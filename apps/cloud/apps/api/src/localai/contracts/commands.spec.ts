import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { COMMAND_TYPES, COMMANDS, isCommandType, type CommandType } from './commands';
import {
  ClaimRequestSchema,
  ClaimResponseSchema,
  FailureRequestSchema,
  RenewRequestSchema,
  RenewResponseSchema,
  ResultRequestSchema,
  SettleResponseSchema,
} from './command-wire';
import { readCommandExamples } from '../../../test/fixtures/localai/command-examples';

const fixtures = join(__dirname, '../../../test/fixtures/localai');
const read = <T = unknown>(name: string): T => JSON.parse(readFileSync(join(fixtures, name), 'utf8')) as T;
const examples = readCommandExamples<Record<string, { params: unknown; result: unknown }>>();

describe('COMMANDS registry', () => {
  it('is the closed set of 17 types, each with a shared example', () => {
    expect(COMMAND_TYPES).toHaveLength(17);
    expect(Object.keys(examples).sort()).toEqual([...COMMAND_TYPES].sort());
  });

  it.each(COMMAND_TYPES)('%s: its example params and result parse', (type) => {
    expect(COMMANDS[type].params.safeParse(examples[type].params).success).toBe(true);
    expect(COMMANDS[type].result.safeParse(examples[type].result).success).toBe(true);
  });

  it.each(COMMAND_TYPES)('%s: an unknown key fails in params and in result', (type) => {
    expect(COMMANDS[type].params.safeParse({ ...(examples[type].params as object), extra: 1 }).success).toBe(false);
    expect(COMMANDS[type].result.safeParse({ ...(examples[type].result as object), extra: 1 }).success).toBe(false);
  });

  it('recognises only the registered, prefixed names', () => {
    expect(isCommandType('localai.model.load')).toBe(true);
    expect(isCommandType('model.load')).toBe(false);
    expect(isCommandType('localai.commands.sweep')).toBe(false);
    expect(isCommandType('toString')).toBe(false);
    expect(isCommandType(undefined)).toBe(false);
  });

  it('logs.tail: lines 0 and 501 fail, 1 and 500 pass, and the default is 200', () => {
    const { params } = COMMANDS['localai.logs.tail'];
    expect(params.safeParse({ unit: 'agent', lines: 0 }).success).toBe(false);
    expect(params.safeParse({ unit: 'agent', lines: 501 }).success).toBe(false);
    expect(params.safeParse({ unit: 'agent', lines: 1 }).success).toBe(true);
    expect(params.safeParse({ unit: 'agent', lines: 500 }).success).toBe(true);
    expect(params.parse({ unit: 'agent' })).toEqual({ unit: 'agent', lines: 200 });
    expect(params.safeParse({ unit: 'shell' }).success).toBe(false);
  });

  it('ark.sync and ark.verify: 200 items pass, 201 fail, an empty list fails', () => {
    const ids = (n: number) => Array.from({ length: n }, (_, i) => `item-${String(i).padStart(3, '0')}`);
    for (const type of ['localai.ark.sync', 'localai.ark.verify'] as const) {
      expect(COMMANDS[type].params.safeParse({ items: ids(200) }).success).toBe(true);
      expect(COMMANDS[type].params.safeParse({ items: ids(201) }).success).toBe(false);
      expect(COMMANDS[type].params.safeParse({ items: [] }).success).toBe(false);
    }
  });

  it('model ids: "Qwen" and too-short or odd ids fail, catalog ids pass', () => {
    const { params } = COMMANDS['localai.model.unload'];
    for (const bad of ['Qwen', 'qw', '-qwen3', 'qwen 3', 'qwen/3', 'a'.repeat(65)]) {
      expect(params.safeParse({ model_id: bad }).success).toBe(false);
    }
    expect(params.safeParse({ model_id: 'qwen3.6-35b-a3b-q4km' }).success).toBe(true);
  });

  it('model.delete: confirm must equal model_id, and roots are required', () => {
    const { params } = COMMANDS['localai.model.delete'];
    const ok = { model_id: 'gemma-4-26b-q4km', roots: ['internal'], confirm: 'gemma-4-26b-q4km' };
    expect(params.safeParse(ok).success).toBe(true);
    expect(params.safeParse({ ...ok, confirm: 'gemma-4-26b' }).success).toBe(false);
    expect(params.safeParse({ ...ok, roots: [] }).success).toBe(false);
    expect(params.safeParse({ model_id: ok.model_id, confirm: ok.confirm }).success).toBe(false);
  });

  it('applies the defaults: wait true, unload_others true, set_default false, dry_run false', () => {
    expect(COMMANDS['localai.model.load'].params.parse({ model_id: 'qwen3.6-35b-a3b-q4km' })).toEqual({
      model_id: 'qwen3.6-35b-a3b-q4km',
      wait: true,
      unload_others: true,
      set_default: false,
    });
    expect(COMMANDS['localai.model.load'].params.safeParse({ model_id: 'qwen3.6-35b-a3b-q4km', wait: false }).success).toBe(false);
    expect(COMMANDS['localai.ark.sync'].params.parse({})).toEqual({ dry_run: false });
  });

  it('root ids, services, semver and key hints are validated', () => {
    expect(COMMANDS['localai.model.download'].params.safeParse({ model_id: 'qwen3.6-35b-a3b-q4km', roots: ['usb'] }).success).toBe(false);
    expect(COMMANDS['localai.model.download'].params.safeParse({ model_id: 'qwen3.6-35b-a3b-q4km', roots: ['ark-0a1b2c3d'] }).success).toBe(true);
    expect(COMMANDS['localai.service.restart'].params.safeParse({ service: 'sshd' }).success).toBe(false);
    expect(COMMANDS['localai.agent.update'].params.safeParse({ version: 'latest' }).success).toBe(false);
    expect(COMMANDS['localai.agent.update'].params.safeParse({ version: '1.2.3-rc.1' }).success).toBe(true);
    const keyResult = examples['localai.key.rotate'].result as Record<string, unknown>;
    expect(COMMANDS['localai.key.rotate'].result.safeParse({ ...keyResult, fingerprint: 'a-full-key-must-never-pass' }).success).toBe(false);
  });

  it('a result never carries a secret-shaped field: key.rotate allows only the fingerprint', () => {
    const keys = Object.keys((COMMANDS['localai.key.rotate'].result as unknown as { shape: Record<string, unknown> }).shape);
    expect(keys.sort()).toEqual(['cloud_synced', 'fingerprint', 'rotated_at']);
  });
});

describe('the E11 commands: workload.activate and music.generate (E11 contract section 6)', () => {
  const music = COMMANDS['localai.music.generate'];
  const example = examples['localai.music.generate'].params as Record<string, any>;
  const without = (key: string) => Object.fromEntries(Object.entries(example).filter(([k]) => k !== key));

  it('workload.activate: a name and an optional force, nothing else; the result names the active workload', () => {
    const { params, result } = COMMANDS['localai.workload.activate'];
    expect(params.parse({ name: 'coding' })).toEqual({ name: 'coding' });
    expect(params.safeParse({ name: 'music', force: true }).success).toBe(true);
    for (const bad of ['', 'Music', '../music', 'a'.repeat(33), 'music now']) expect(params.safeParse({ name: bad }).success).toBe(false);
    expect(params.safeParse({ name: 'music', wait: true }).success).toBe(false);
    expect(result.safeParse({ active: 'music' }).success).toBe(true);
    expect(result.safeParse({ active: null }).success).toBe(false);
  });

  it('music.generate: an instrumental request with no sample parses (lyrics may be empty)', () => {
    const plain = { ...without('sample'), lyrics: '', results: example.results.slice(0, 1), variations: 1 };
    delete (plain as Record<string, unknown>).sample_mode;
    delete (plain as Record<string, unknown>).strength;
    expect(music.params.safeParse(plain).success).toBe(true);
  });

  it('music.generate: one result URL per variation, 1 to 4', () => {
    expect(music.params.safeParse({ ...example, variations: 1 }).success).toBe(false);
    expect(music.params.safeParse({ ...example, variations: 5, results: Array(5).fill(example.results[0]) }).success).toBe(false);
    expect(music.params.safeParse({ ...example, variations: 0, results: [] }).success).toBe(false);
  });

  it('music.generate: lyrics up to 8 KiB and tags up to 512 bytes, counted in UTF-8 bytes', () => {
    expect(music.params.safeParse({ ...example, lyrics: 'a'.repeat(8192) }).success).toBe(true);
    expect(music.params.safeParse({ ...example, lyrics: 'a'.repeat(8193) }).success).toBe(false);
    expect(music.params.safeParse({ ...example, lyrics: 'ñ'.repeat(4097) }).success).toBe(false); // 8194 bytes
    expect(music.params.safeParse({ ...example, tags: 't'.repeat(512) }).success).toBe(true);
    expect(music.params.safeParse({ ...example, tags: 't'.repeat(513) }).success).toBe(false);
  });

  it('music.generate: duration 10 to 240 s, a language tag, a 32-bit seed', () => {
    for (const ok of [10, 240]) expect(music.params.safeParse({ ...example, duration_s: ok }).success).toBe(true);
    for (const bad of [9, 241, 60.5]) expect(music.params.safeParse({ ...example, duration_s: bad }).success).toBe(false);
    expect(music.params.safeParse({ ...example, language: 'english' }).success).toBe(false);
    expect(music.params.safeParse({ ...example, seed: -1 }).success).toBe(false);
    expect(music.params.safeParse(without('seed')).success).toBe(true);
  });

  it('music.generate: the URLs are https only, and the sample is a 20 MiB at most wav/mp3/flac/ogg with a sha256', () => {
    const http = example.results[0].put_url.replace('https://', 'http://');
    expect(music.params.safeParse({ ...example, results: [{ put_url: http }, example.results[1]] }).success).toBe(false);
    expect(music.params.safeParse({ ...example, sample: { ...example.sample, url: example.sample.url.replace('https://', 'http://') } }).success).toBe(false);
    expect(music.params.safeParse({ ...example, sample: { ...example.sample, size_bytes: 20 * 1024 * 1024 + 1 } }).success).toBe(false);
    expect(music.params.safeParse({ ...example, sample: { ...example.sample, mime: 'video/mp4' } }).success).toBe(false);
    expect(music.params.safeParse({ ...example, sample: { ...example.sample, sha256: 'abc' } }).success).toBe(false);
    expect(music.params.safeParse({ ...example, sample: { ...example.sample, extra: 1 } }).success).toBe(false);
    expect(music.params.safeParse({ ...example, results: [{ put_url: example.results[0].put_url, method: 'PUT' }, example.results[1]] }).success).toBe(false);
  });

  it('music.generate: sample_mode and strength need a sample; strength is 0 to 1', () => {
    expect(music.params.safeParse(without('sample')).success).toBe(false);
    expect(music.params.safeParse({ ...example, strength: 1.5 }).success).toBe(false);
    expect(music.params.safeParse({ ...example, sample_mode: 'remix' }).success).toBe(false);
  });

  it('music.generate result: 1 to 4 songs, each with its index, sha256, size, an audio mime and a duration', () => {
    const songs = (examples['localai.music.generate'].result as { songs: Array<Record<string, unknown>> }).songs;
    expect(music.result.safeParse({ songs: [] }).success).toBe(false);
    expect(music.result.safeParse({ songs: [{ ...songs[0], index: 4 }] }).success).toBe(false);
    expect(music.result.safeParse({ songs: [{ ...songs[0], mime: 'text/html' }] }).success).toBe(false);
    expect(music.result.safeParse({ songs: [{ ...songs[0], path: '/var/lib/localai/music/songs/x/audio.wav' }] }).success).toBe(false);
  });
});

describe('the claim, renew, result and failure wire shapes (the shared fixtures)', () => {
  it('the claim request fixture parses and every name in it is a known command (without the prefix)', () => {
    const body = ClaimRequestSchema.parse(read('claim.request.json'));
    for (const name of body.types) expect(isCommandType(`localai.${name}`)).toBe(true);
    expect(body.limit).toBe(1);
    expect(body.waitMs).toBe(25000);
  });

  it('the claim response fixtures parse, and the claimed params parse under their type', () => {
    const empty = read<{ data: unknown }>('claim.response.empty.json');
    expect(ClaimResponseSchema.parse(empty.data).jobs).toEqual([]);
    const claimed = ClaimResponseSchema.parse(read<{ data: unknown }>('claim.response.model-load.json').data);
    expect(claimed.jobs).toHaveLength(1);
    const type = claimed.jobs[0].job.type as CommandType;
    expect(isCommandType(type)).toBe(true);
    expect(COMMANDS[type].params.safeParse(claimed.jobs[0].params).success).toBe(true);
  });

  it('the renew, result and failure fixtures parse; their answers parse', () => {
    expect(RenewRequestSchema.safeParse(read('renew.request.json')).success).toBe(true);
    expect(RenewResponseSchema.safeParse(read<{ data: unknown }>('renew.response.json').data).success).toBe(true);
    const result = ResultRequestSchema.parse(read('result.request.model-load.json'));
    expect(COMMANDS[result.type as CommandType].result.safeParse(result.result).success).toBe(true);
    expect(SettleResponseSchema.parse(read<{ data: unknown }>('result.response.json').data)).toEqual({ status: 'succeeded' });
    expect(FailureRequestSchema.safeParse(read('failure.request.json')).success).toBe(true);
    expect(SettleResponseSchema.parse(read<{ data: unknown }>('failure.response.json').data)).toEqual({ status: 'failed' });
  });

  it('request bodies are strict, except the two fields of EAB\'s failure shape that are accepted and ignored', () => {
    expect(ClaimRequestSchema.safeParse({ types: [], extra: true }).success).toBe(false);
    expect(RenewRequestSchema.safeParse({ claimToken: 'x', extra: true }).success).toBe(false);
    expect(ResultRequestSchema.safeParse({ type: 'localai.x', claimToken: 'x', result: {}, extra: true }).success).toBe(false);
    expect(ResultRequestSchema.safeParse({ type: 'localai.x', claimToken: 'x' }).success).toBe(false);
    expect(FailureRequestSchema.safeParse({ claimToken: 'x', error: 'boom', rateLimited: true, retryAfterMs: 5 }).success).toBe(true);
    expect(FailureRequestSchema.safeParse({ claimToken: 'x', error: 'a'.repeat(2001) }).success).toBe(false);
    expect(FailureRequestSchema.safeParse({ claimToken: 'x', error: 'a'.repeat(2000) }).success).toBe(true);
  });
});
