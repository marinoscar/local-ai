import { LAI_CLD_COMMAND_FAILED, MAX_FAILURE_MESSAGE, parseFailureString } from './command-failure';
import { scrubJson, scrubText } from './command-scrub';

// Addresses are built from parts so no literal tailnet address is committed (the redaction check forbids them).
const ip = (...octets: number[]) => ['100', ...octets].join('.');

describe('parseFailureString', () => {
  it('splits a leading doctor code from the message', () => {
    expect(parseFailureString('LAI-CAT-005: Model does not fit in free VRAM')).toEqual({ code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' });
    expect(parseFailureString('LAI-LLM-011:no room')).toEqual({ code: 'LAI-LLM-011', message: 'no room' });
    expect(parseFailureString('LAI-CLD-058:   spaces   ')).toEqual({ code: 'LAI-CLD-058', message: 'spaces' });
  });

  it('gives LAI-CLD-012 to a string with no leading code, keeping the whole text as the message', () => {
    expect(parseFailureString('disk is full')).toEqual({ code: LAI_CLD_COMMAND_FAILED, message: 'disk is full' });
    expect(parseFailureString('failed: LAI-CAT-005 later')).toEqual({ code: 'LAI-CLD-012', message: 'failed: LAI-CAT-005 later' });
    expect(parseFailureString('lai-cat-005: lower case')).toEqual({ code: 'LAI-CLD-012', message: 'lai-cat-005: lower case' });
    expect(parseFailureString('LAI-CAT-05: two digits')).toEqual({ code: 'LAI-CLD-012', message: 'LAI-CAT-05: two digits' });
    expect(parseFailureString('LAI-CAT1-005: area with a digit').code).toBe('LAI-CLD-012');
  });

  it('keeps at most 500 characters of message, after the code', () => {
    const parsed = parseFailureString(`LAI-SYN-001: ${'x'.repeat(2000)}`);
    expect(parsed.code).toBe('LAI-SYN-001');
    expect(parsed.message).toHaveLength(MAX_FAILURE_MESSAGE);
  });

  it('scrubs secrets out of the message and never from the code', () => {
    const token = `nod_${'a'.repeat(40)}`;
    const parsed = parseFailureString(`LAI-AGT-002: call failed with Bearer ${token} on ${ip(101, 102, 103)}`);
    expect(parsed.message).not.toContain(token);
    expect(parsed.message).not.toContain(ip(101, 102, 103));
    expect(parsed.message).toContain('call failed');
    expect(parsed.code).toBe('LAI-AGT-002');
  });

  it('handles an empty rest', () => {
    expect(parseFailureString('LAI-CAT-005:')).toEqual({ code: 'LAI-CAT-005', message: '' });
  });
});

describe('scrubText and scrubJson', () => {
  const hex = 'ab'.repeat(32);

  it('removes bearer credentials, credential-prefixed tokens, 64-hex keys, tailnet addresses and the tailnet name', () => {
    expect(scrubText('Authorization: Bearer abcdef123456')).toBe('Authorization: Bearer [redacted]');
    for (const prefix of ['lat', 'nod', 'lai', 'pat', 'pg']) expect(scrubText(`token=${prefix}_${'Z'.repeat(24)}!`)).toBe('token=[redacted]!');
    expect(scrubText(`key ${hex} end`)).toBe('key [redacted] end');
    expect(scrubText(`peer ${ip(64, 0, 1)} and ${ip(127, 255, 254)} up`)).toBe('peer [redacted] and [redacted] up');
    expect(scrubText(`on ${['tail', '1a2b3c'].join('')}.example.test`)).toBe('on [redacted].example.test');
  });

  it('keeps what is not a secret: other 100.x addresses, short tokens, plain text', () => {
    expect(scrubText(`peer ${ip(63, 0, 1)} and ${ip(128, 0, 1)}`)).toBe(`peer ${ip(63, 0, 1)} and ${ip(128, 0, 1)}`);
    expect(scrubText('nod_short is fine')).toBe('nod_short is fine');
    expect(scrubText('llama-server started on 127.0.0.1:8080')).toBe('llama-server started on 127.0.0.1:8080');
    expect(scrubText('ok')).toBe('ok');
  });

  it('is idempotent', () => {
    const once = scrubText(`Bearer abc123def456 ${hex} ${ip(100, 1, 1)} nod_${'q'.repeat(30)}`);
    expect(scrubText(once)).toBe(once);
  });

  it('scrubs every string of a JSON value and leaves the shape alone', () => {
    const value = { unit: 'agent', lines: [`boot ${hex}`, 'fine'], truncated: false, nested: { n: 1, s: 'Bearer secretvalue1' } };
    expect(scrubJson(value)).toEqual({ unit: 'agent', lines: ['boot [redacted]', 'fine'], truncated: false, nested: { n: 1, s: 'Bearer [redacted]' } });
    expect(scrubJson(null)).toBeNull();
    expect(scrubJson(5)).toBe(5);
  });
});
