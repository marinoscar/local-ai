// The transcription route's own pieces: the multipart plugin registration (with and without the application's own
// registration of the plugin), the field allowlist, the filename and boundary rules, and the audio capability check.
import multipart from '@fastify/multipart';
import Fastify, { type FastifyInstance } from 'fastify';

import { GatewayError } from './errors';
import {
  acceptsAudio,
  audioCapabilityOf,
  AUDIO_FILE_LIMIT,
  buildTranscriptionBody,
  multipartErrorToGatewayError,
  readTranscriptionUpload,
  registerAudioMultipart,
  safeFilename,
  TRANSCRIPTION_FIELDS,
  type TranscriptionUpload,
} from './transcriptions-route';

function form(parts: Array<{ name: string; value?: string; data?: Buffer }>): { payload: Buffer; headers: Record<string, string> } {
  const boundary = 'unit-boundary';
  const chunks: Buffer[] = [];
  for (const part of parts) {
    if (part.data) chunks.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${part.name}"; filename="a.wav"\r\nContent-Type: audio/wav\r\n\r\n`), part.data, Buffer.from('\r\n'));
    else chunks.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${part.name}"\r\n\r\n${part.value}\r\n`));
  }
  chunks.push(Buffer.from(`--${boundary}--\r\n`));
  return { payload: Buffer.concat(chunks), headers: { 'content-type': `multipart/form-data; boundary=${boundary}` } };
}

async function app(options: { appRegistersPlugin?: { fileSize: number } } = {}): Promise<FastifyInstance> {
  const instance = Fastify();
  // The application (src/main.ts) registers the plugin for its own upload routes, with its own limits.
  if (options.appRegistersPlugin) await instance.register(multipart, { limits: { fileSize: options.appRegistersPlugin.fileSize, files: 1 } });
  await instance.register(async (scope) => {
    await scope.register(async (child) => {
      await registerAudioMultipart(child);
      child.post('/upload', async (request) => {
        try {
          const upload = await readTranscriptionUpload(request);
          return { model: upload.model, fields: upload.fields, bytes: upload.file.data.length };
        } catch (error) {
          const gatewayError = error as GatewayError;
          return { error: gatewayError.localaiCode };
        }
      });
    });
  });
  await instance.ready();
  return instance;
}

describe.each([
  ['the plugin registered for this route only', undefined],
  ["the application's own registration of the plugin (small limits) already present", { fileSize: 8 }],
] as const)('readTranscriptionUpload with %s', (_name, appRegistersPlugin) => {
  it('reads the fields and the file under the endpoint\'s own limits, not the application\'s', async () => {
    const instance = await app({ appRegistersPlugin });
    const body = form([{ name: 'model', value: 'stt-a' }, { name: 'language', value: 'es' }, { name: 'file', data: Buffer.alloc(100_000, 3) }]);
    const response = await instance.inject({ method: 'POST', url: '/upload', ...body });
    expect(response.json()).toEqual({ model: 'stt-a', fields: [{ name: 'model', value: 'stt-a' }, { name: 'language', value: 'es' }], bytes: 100_000 });
    await instance.close();
  });

  it('refuses a file over the file limit', async () => {
    const instance = await app({ appRegistersPlugin });
    const body = form([{ name: 'model', value: 'stt-a' }, { name: 'file', data: Buffer.alloc(AUDIO_FILE_LIMIT + 1, 3) }]);
    const response = await instance.inject({ method: 'POST', url: '/upload', ...body });
    expect(response.json()).toEqual({ error: 'LAI-GW-004' });
    await instance.close();
  });
});

describe('the field allowlist', () => {
  it('is exactly the OpenAI fields', () => {
    expect([...TRANSCRIPTION_FIELDS].sort()).toEqual(['file', 'language', 'model', 'prompt', 'response_format', 'temperature', 'timestamp_granularities[]']);
  });
});

describe('multipartErrorToGatewayError', () => {
  it.each([
    ['FST_REQ_FILE_TOO_LARGE', 'LAI-GW-004'],
    ['FST_FILES_LIMIT', 'LAI-GW-003'],
    ['FST_FIELDS_LIMIT', 'LAI-GW-003'],
    ['FST_PARTS_LIMIT', 'LAI-GW-003'],
    ['FST_INVALID_MULTIPART_CONTENT_TYPE', 'LAI-GW-001'],
    ['SOMETHING_ELSE', 'LAI-GW-001'],
  ])('%s -> %s', (code, expected) => expect(multipartErrorToGatewayError(Object.assign(new Error('quotes the body: secret'), { code })).localaiCode).toBe(expected));

  it('never uses the message of the error', () => {
    expect(multipartErrorToGatewayError(Object.assign(new Error('quotes the body: secret'), { code: 'X' })).message).not.toContain('secret');
  });

  it('passes a gateway error through', () => {
    const original = new GatewayError('LAI-GW-002');
    expect(multipartErrorToGatewayError(original)).toBe(original);
  });
});

describe('safeFilename', () => {
  it.each([
    ['speech.wav', 'speech.wav'],
    ['a"b.wav', 'a_b.wav'],
    ['a\r\nX-Evil: 1.wav', 'a__X-Evil: 1.wav'],
    ['dir\\file.wav', 'dir_file.wav'],
    ['vozñ.wav', 'voz_.wav'],
    ['', 'audio'],
    ['   ', 'audio'],
  ])('%j -> %j', (input, expected) => expect(safeFilename(input)).toBe(expected));
  it('is at most 200 characters', () => expect(safeFilename('x'.repeat(500))).toHaveLength(200));
});

describe('buildTranscriptionBody', () => {
  const upload = (data: Buffer, overrides: Partial<TranscriptionUpload> = {}): TranscriptionUpload => ({
    model: 'alias',
    fields: [{ name: 'model', value: 'alias' }, { name: 'language', value: 'es' }],
    file: { filename: 'a.wav', mimetype: 'audio/wav', data },
    ...overrides,
  });

  it('picks a boundary that is in no part, even when the audio contains something boundary-shaped', () => {
    const data = Buffer.from('----localai-gw-00000000000000000000000000000000 and more');
    const built = buildTranscriptionBody(upload(data), 'stt-a');
    const boundary = /boundary=(.+)$/.exec(built.contentType)![1]!;
    expect(data.includes(Buffer.from(boundary))).toBe(false);
    expect(built.body.includes(data)).toBe(true);
  });

  it('uses a new boundary every time', () => {
    const a = buildTranscriptionBody(upload(Buffer.from('x')), 'stt-a').contentType;
    const b = buildTranscriptionBody(upload(Buffer.from('x')), 'stt-a').contentType;
    expect(a).not.toBe(b);
  });

  it('replaces the model in place and keeps the other fields and their order', () => {
    const text = buildTranscriptionBody(upload(Buffer.from('x')), 'stt-a').body.toString('utf8');
    expect(text.indexOf('name="model"')).toBeLessThan(text.indexOf('name="language"'));
    expect(text).toContain('name="model"\r\n\r\nstt-a\r\n');
    expect(text).not.toContain('alias');
  });
});

describe('audio capability', () => {
  const table = (modalities: string[][]) => ({ healthyCandidates: () => modalities.map((inputModalities) => ({ inputModalities })) });

  it('acceptsAudio reads the reported modalities', () => {
    expect(acceptsAudio({ inputModalities: ['text', 'audio'] })).toBe(true);
    expect(acceptsAudio({ inputModalities: ['text'] })).toBe(false);
    expect(acceptsAudio({ inputModalities: [] })).toBe(false);
  });

  it.each([
    [[], 'unknown'],
    [[['text']], 'no_audio'],
    [[[]], 'no_audio'],
    [[['text'], ['text', 'audio']], 'audio'],
  ] as const)('%j -> %s', (modalities, expected) => expect(audioCapabilityOf(table(modalities.map((m) => [...m]))).availability('m')).toBe(expected));
});
