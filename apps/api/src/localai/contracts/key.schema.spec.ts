import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { HEX64, testKey } from '../../../test/localai/support/capture-output';
import { fingerprintOf, NodeKeyResponseSchema, parseNodeKeyRequest } from './key.schema';

const fixtureRequest = JSON.parse(readFileSync(join(__dirname, '../../../test/fixtures/localai/key.request.json'), 'utf8')) as Record<string, string>;
const fixtureResponse = JSON.parse(readFileSync(join(__dirname, '../../../test/fixtures/localai/key.response.json'), 'utf8')) as { data: unknown };

/** The committed fixture with its placeholders replaced by a key built at runtime. */
function validBody(key = testKey()): Record<string, unknown> {
  return { ...fixtureRequest, key, fingerprint: fingerprintOf(key) };
}

describe('fingerprintOf', () => {
  it('is the first four and last four characters around an ellipsis', () => {
    const key = `ab12${'0'.repeat(56)}9f3c`;
    expect(fingerprintOf(key)).toBe('ab12…9f3c');
  });
});

describe('parseNodeKeyRequest', () => {
  it('accepts the fixture shape with a runtime-built key', () => {
    const body = validBody();
    const parsed = parseNodeKeyRequest(body);
    expect(parsed.ok).toBe(true);
    if (parsed.ok) expect(parsed.value).toEqual(body);
  });

  it('keeps the committed fixture free of key material', () => {
    expect(JSON.stringify(fixtureRequest)).not.toMatch(HEX64);
    expect(parseNodeKeyRequest(fixtureRequest).ok).toBe(false);
  });

  const key = testKey();
  const refused: Array<[string, unknown, string[]]> = [
    ['a 63 character key', { ...validBody(key), key: key.slice(1) }, ['key']],
    ['a 65 character key', { ...validBody(key), key: `${key}0` }, ['key']],
    ['an upper-case key', { ...validBody(key), key: key.toUpperCase() }, ['key']],
    ['a non-hex key', { ...validBody(key), key: `${'z'.repeat(64)}` }, ['key']],
    ['a key with a trailing newline', { ...validBody(key), key: `${key}\n` }, ['key']],
    ['a missing key', { fingerprint: fingerprintOf(key), rotated_at: '2026-11-02T15:04:05Z' }, ['key']],
    ['a number for a key', { ...validBody(key), key: 12345 }, ['key']],
    ['a malformed fingerprint', { ...validBody(key), fingerprint: 'ab12-9f3c' }, ['fingerprint']],
    ['a missing rotated_at', { key, fingerprint: fingerprintOf(key) }, ['rotated_at']],
    ['a rotated_at that is not a date', { ...validBody(key), rotated_at: 'yesterday' }, ['rotated_at']],
    ['an unknown field', { ...validBody(key), extra: 'x' }, []],
    ['null', null, []],
    ['an array', [validBody(key)], []],
    ['a string', 'key', []],
  ];

  it.each(refused)('refuses %s with kind "invalid"', (_name, body, paths) => {
    const parsed = parseNodeKeyRequest(body);
    expect(parsed.ok).toBe(false);
    if (parsed.ok) return;
    expect(parsed.refusal.kind).toBe('invalid');
    expect(parsed.refusal.issues.map((issue) => issue.path.join('.'))).toEqual(expect.arrayContaining(paths.map((p) => p)));
  });

  it('refuses a fingerprint that is well formed but not the key\'s with kind "fingerprint_mismatch"', () => {
    const other = testKey();
    const parsed = parseNodeKeyRequest({ ...validBody(key), fingerprint: fingerprintOf(other) });
    expect(parsed.ok).toBe(false);
    if (!parsed.ok) expect(parsed.refusal).toEqual({ kind: 'fingerprint_mismatch', issues: [{ path: ['fingerprint'], code: 'fingerprint_mismatch' }] });
  });

  it('never puts a received value in a refusal, whatever the field', () => {
    const secret = testKey();
    const cases: unknown[] = [
      { ...validBody(secret), key: secret.slice(1) },
      { ...validBody(secret), fingerprint: 'nope' },
      { ...validBody(secret), extra: secret },
      { ...validBody(secret), [secret]: 'x' },
      { ...validBody(secret), fingerprint: fingerprintOf(testKey()) },
      { key: secret.toUpperCase(), fingerprint: secret, rotated_at: secret },
    ];
    for (const body of cases) {
      const parsed = parseNodeKeyRequest(body);
      expect(parsed.ok).toBe(false);
      if (!parsed.ok) {
        const text = JSON.stringify(parsed.refusal);
        expect(text).not.toContain(secret);
        expect(text).not.toContain(secret.toUpperCase());
        expect(text).not.toMatch(HEX64);
      }
    }
  });
});

describe('NodeKeyResponseSchema', () => {
  it('accepts the fixture and refuses any extra field (so a key can never ride along)', () => {
    expect(NodeKeyResponseSchema.safeParse(fixtureResponse.data).success).toBe(true);
    expect(NodeKeyResponseSchema.safeParse({ ...(fixtureResponse.data as object), key: testKey() }).success).toBe(false);
  });
});
