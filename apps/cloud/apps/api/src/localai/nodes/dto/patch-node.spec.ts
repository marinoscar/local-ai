import { isTailnetAddress, PatchNodeBodySchema, RetireNodeBodySchema } from './patch-node';

// Addresses are built from parts so no literal tailnet address sits in the repository.
const ip4 = (...parts: number[]) => parts.join('.');
const IP6 = `${['fd7a', '115c', 'a1e0'].join(':')}::1`;

describe('PatchNodeBodySchema', () => {
  it('accepts every editable field with its documented shape', () => {
    const parsed = PatchNodeBodySchema.parse({
      displayName: '  Node Two  ',
      publicHost: 'AI-N2.example.test',
      servePattern: 'B',
      tailnetMachine: 'Machine-Two',
      tailnetIp: ip4(100, 64, 1, 2),
      gatewayEnabled: false,
      gatewayPriority: 50,
      catalogPin: '2026.11.02-1',
    });
    expect(parsed).toEqual({
      displayName: 'Node Two',
      publicHost: 'ai-n2.example.test',
      servePattern: 'B',
      tailnetMachine: 'machine-two',
      tailnetIp: ip4(100, 64, 1, 2),
      gatewayEnabled: false,
      gatewayPriority: 50,
      catalogPin: '2026.11.02-1',
    });
  });

  it.each(['A', 'B', 'none'])('accepts servePattern %s', (servePattern) => {
    expect(PatchNodeBodySchema.safeParse({ servePattern }).success).toBe(true);
  });

  it.each(['C', 'a', '', 'None', 1])('rejects servePattern %p', (servePattern) => {
    expect(PatchNodeBodySchema.safeParse({ servePattern }).success).toBe(false);
  });

  it.each(['localhost', 'ai n2.example.test', 'https://ai-n2.example.test', '-bad.example.test', 'ai-n2..example.test', `${'a'.repeat(64)}.example.test`, ''])('rejects publicHost %p', (publicHost) => {
    expect(PatchNodeBodySchema.safeParse({ publicHost }).success).toBe(false);
  });

  it.each([1.5, -1, 10_001, '5', Number.NaN])('rejects gatewayPriority %p', (gatewayPriority) => {
    expect(PatchNodeBodySchema.safeParse({ gatewayPriority }).success).toBe(false);
  });

  it('accepts a whole gatewayPriority between 0 and 10000', () => {
    for (const gatewayPriority of [0, 100, 10_000]) expect(PatchNodeBodySchema.safeParse({ gatewayPriority }).success).toBe(true);
  });

  it('turns a blank nullable text into null (clear it)', () => {
    expect(PatchNodeBodySchema.parse({ tailnetMachine: '  ', tailnetIp: '', catalogPin: '' })).toEqual({ tailnetMachine: null, tailnetIp: null, catalogPin: null });
  });

  it('rejects a tailnet address outside the tailnet ranges, and a malformed catalog pin', () => {
    expect(PatchNodeBodySchema.safeParse({ tailnetIp: ip4(192, 168, 1, 5) }).success).toBe(false);
    expect(PatchNodeBodySchema.safeParse({ tailnetIp: 'not an address' }).success).toBe(false);
    expect(PatchNodeBodySchema.safeParse({ catalogPin: 'latest' }).success).toBe(false);
  });

  it('rejects an unknown key, an empty body and a name that is blank or too long', () => {
    expect(PatchNodeBodySchema.safeParse({ nodeId: 'ai-n9' }).success).toBe(false);
    expect(PatchNodeBodySchema.safeParse({ phase: 'retired' }).success).toBe(false);
    expect(PatchNodeBodySchema.safeParse({}).success).toBe(false);
    expect(PatchNodeBodySchema.safeParse({ displayName: '   ' }).success).toBe(false);
    expect(PatchNodeBodySchema.safeParse({ displayName: 'x'.repeat(81) }).success).toBe(false);
  });
});

describe('isTailnetAddress', () => {
  it('knows the IPv4 CGNAT edges and the Tailscale IPv6 prefix', () => {
    expect(isTailnetAddress(ip4(100, 64, 0, 0))).toBe(true);
    expect(isTailnetAddress(ip4(100, 127, 255, 255))).toBe(true);
    expect(isTailnetAddress(ip4(100, 63, 255, 255))).toBe(false);
    expect(isTailnetAddress(ip4(100, 128, 0, 1))).toBe(false);
    expect(isTailnetAddress(IP6)).toBe(true);
    expect(isTailnetAddress('fe80::1')).toBe(false);
  });
});

describe('RetireNodeBodySchema', () => {
  it('needs a confirm string and nothing else', () => {
    expect(RetireNodeBodySchema.safeParse({ confirm: 'ai-n2' }).success).toBe(true);
    expect(RetireNodeBodySchema.safeParse({}).success).toBe(false);
    expect(RetireNodeBodySchema.safeParse({ confirm: 'ai-n2', force: true }).success).toBe(false);
  });
});
