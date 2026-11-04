import { clientWantsUsageChunk, rewriteBody, serialiseBody } from './body-rewrite';

const body = {
  model: 'lab-coder',
  messages: [{ role: 'user', content: [{ type: 'text', text: 'hi' }, { type: 'image_url', image_url: { url: 'data:image/png;base64,AAAA' } }] }],
  temperature: 0.25,
  top_p: 1,
  max_tokens: 128,
  stop: ['\n', '###'],
  logit_bias: { '50256': -100 },
  seed: null,
  tools: [{ type: 'function', function: { name: 'f', parameters: { type: 'object', properties: { a: { type: 'integer' } } } } }],
  n: 1,
  unicode: 'ñandú ✓ 日本語',
};

describe('rewriteBody', () => {
  it('changes only model for a non-stream request; everything else round-trips equal', () => {
    const out = JSON.parse(serialiseBody(body, { modelId: 'model-a', stream: false, forceUsageChunk: true }).toString('utf8'));
    expect(out).toEqual({ ...body, model: 'model-a' });
    expect(Object.keys(out)).toEqual(Object.keys(body));
  });

  it('sets stream_options.include_usage on chat streams and keeps the rest of the client stream_options', () => {
    const rewritten = rewriteBody({ ...body, stream: true, stream_options: { include_usage: false, other: 1 } }, { modelId: 'm', stream: true, forceUsageChunk: true });
    expect(rewritten.stream_options).toEqual({ include_usage: true, other: 1 });
    expect(rewriteBody({ ...body, stream: true }, { modelId: 'm', stream: true, forceUsageChunk: true }).stream_options).toEqual({ include_usage: true });
    expect(rewriteBody({ ...body, stream: true, stream_options: 'junk' }, { modelId: 'm', stream: true, forceUsageChunk: true }).stream_options).toEqual({ include_usage: true });
  });

  it('does not add stream_options to a non-stream request or to /v1/completions', () => {
    expect(rewriteBody(body, { modelId: 'm', stream: false, forceUsageChunk: true })).not.toHaveProperty('stream_options');
    expect(rewriteBody({ ...body, stream: true }, { modelId: 'm', stream: true, forceUsageChunk: false })).not.toHaveProperty('stream_options');
  });

  it('leaves the parsed input untouched', () => {
    const input = { model: 'a', stream: true, stream_options: { x: 1 } };
    rewriteBody(input, { modelId: 'b', stream: true, forceUsageChunk: true });
    expect(input).toEqual({ model: 'a', stream: true, stream_options: { x: 1 } });
  });
});

describe('clientWantsUsageChunk', () => {
  it.each([
    [{ stream_options: { include_usage: true } }, true],
    [{ stream_options: { include_usage: false } }, false],
    [{ stream_options: 'x' }, false],
    [{}, false],
  ])('%j', (input, expected) => expect(clientWantsUsageChunk(input)).toBe(expected));
});
