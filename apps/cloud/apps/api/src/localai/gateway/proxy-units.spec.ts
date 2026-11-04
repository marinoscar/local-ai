import { keyAllowsModel } from './model-resolver';
import { DEFAULT_GATEWAY_TIMEOUTS, parseProxySettings } from './proxy-settings';
import { parseUsage } from './usage/usage-sink';
import { isValidPublicHost } from './upstream/client';
import { upstreamModeFromEnv } from './upstream/transport.factory';

describe('parseUsage', () => {
  it.each([
    [{ usage: { prompt_tokens: 3, completion_tokens: 5, total_tokens: 999 } }, { promptTokens: 3, completionTokens: 5 }],
    [{ usage: { prompt_tokens: 7 } }, { promptTokens: 7, completionTokens: 0 }],
    [{ usage: { prompt_tokens: -1, completion_tokens: 1 } }, null],
    [{ usage: { prompt_tokens: 1.5 } }, null],
    [{ usage: { completion_tokens: 2 } }, null],
    [{ usage: 'x' }, null],
    [{}, null],
    [null, null],
    ['text', null],
  ])('%j', (input, expected) => expect(parseUsage(input)).toEqual(expected));
});

describe('parseProxySettings', () => {
  it('defaults', () => {
    expect(parseProxySettings(undefined)).toEqual({ timeouts: DEFAULT_GATEWAY_TIMEOUTS, maxAttempts: 3 });
  });
  it('reads the nested and the dotted form and ignores out-of-range values', () => {
    expect(parseProxySettings({ gateway: { maxAttempts: 5, timeouts: { connectMs: 1000, firstByteMs: 5 } } })).toEqual({
      timeouts: { ...DEFAULT_GATEWAY_TIMEOUTS, connectMs: 1000 },
      maxAttempts: 5,
    });
    expect(parseProxySettings({ 'gateway.maxAttempts': 2, 'gateway.timeouts': { idleMs: 4000 } })).toEqual({ timeouts: { ...DEFAULT_GATEWAY_TIMEOUTS, idleMs: 4000 }, maxAttempts: 2 });
    expect(parseProxySettings({ gateway: { maxAttempts: 0 } }).maxAttempts).toBe(3);
  });
});

describe('keyAllowsModel (spec 4.4 scope rule)', () => {
  it.each([
    [[], 'lab-coder', 'qwen', true],
    [['qwen'], 'qwen', 'qwen', true],
    [['qwen'], 'lab-coder', 'qwen', true],
    [['lab-coder'], 'lab-coder', 'qwen', true],
    [['lab-coder'], 'qwen', 'qwen', false],
    [['lab-coder'], 'other-alias', 'qwen', false],
    [['other'], 'qwen', 'qwen', false],
  ])('%j requested %s (id %s)', (allowed, requested, id, expected) => expect(keyAllowsModel(allowed, requested, id)).toBe(expected));
});

describe('SSRF guard on the node host', () => {
  it.each([
    ['ai-n1.example.test', true],
    ['localhost', true],
    ['a.b-c.d', true],
    ['evil.test/path', false],
    ['evil.test:8080', false],
    ['userinfo' + String.fromCharCode(64) + 'evil.test', false],
    ['[::1]', false],
    ['', false],
    ['bad host', false],
    ['-lead.test', false],
  ])('%s', (host, expected) => expect(isValidPublicHost(host)).toBe(expected));
});

describe('upstream mode', () => {
  it('defaults to public', () => {
    expect(upstreamModeFromEnv({})).toBe('public');
    expect(upstreamModeFromEnv({ LOCALAI_GATEWAY_UPSTREAM: 'Tailnet ' })).toBe('tailnet');
    expect(upstreamModeFromEnv({ LOCALAI_GATEWAY_UPSTREAM: 'x' })).toBe('public');
  });
});
