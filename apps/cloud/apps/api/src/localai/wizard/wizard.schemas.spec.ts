import { ChecklistActionBodySchema, CHECKLIST_STEPS, CreateNodeBodySchema, isChecklistStep, isTailnetAddress } from './wizard.schemas';

// Addresses are built from parts so no literal address sits in the repository.
const v4 = (second: number, third = 1, fourth = 2) => ['100', second, third, fourth].join('.');
const LAN_IP = ['192', '168', '1', '5'].join('.');

describe('isTailnetAddress', () => {
  it('accepts the CGNAT block (second octet 64 to 127)', () => {
    for (const second of [64, 100, 127]) expect(isTailnetAddress(v4(second))).toBe(true);
  });
  it('refuses everything else', () => {
    for (const value of [v4(63), v4(128), v4(64, 256), v4(64, 1, 256), '10.0.0.1', LAN_IP, '100.64.1', v4(64) + '.3', '', ' ' + v4(64), 'fd7a:115c:a1e1::1', 'localhost']) {
      expect(isTailnetAddress(value)).toBe(false);
    }
  });
  it('accepts a Tailscale IPv6 address', () => expect(isTailnetAddress(['fd7a', '115c', 'a1e0', '0', '0', '1234', '5678'].join(':'))).toBe(true));
});

describe('CreateNodeBodySchema', () => {
  it('needs only a node id; Pattern B is the default', () => {
    expect(CreateNodeBodySchema.parse({ nodeId: 'ai-n4' })).toEqual({ nodeId: 'ai-n4', servePattern: 'B' });
  });
  it('treats blank optional fields as absent (the form sends empty strings)', () => {
    expect(CreateNodeBodySchema.parse({ nodeId: 'ai-n4', displayName: '  ', publicHost: '', tailnetMachine: '', tailnetIp: '', profile: '' })).toEqual({ nodeId: 'ai-n4', servePattern: 'B' });
  });
  it('keeps the fields it is given, host lower-cased and trimmed', () => {
    const body = CreateNodeBodySchema.parse({ nodeId: 'ai-n4', displayName: ' Node Four ', publicHost: ' AI-N4.Marin.CR ', servePattern: 'A', tailnetMachine: 'host-alpha', tailnetIp: v4(70), profile: 'cuda-12gb' });
    expect(body).toEqual({ nodeId: 'ai-n4', displayName: 'Node Four', publicHost: 'ai-n4.marin.cr', servePattern: 'A', tailnetMachine: 'host-alpha', tailnetIp: v4(70), profile: 'cuda-12gb' });
  });
  it.each([
    { nodeId: 'ai-n04' },
    { nodeId: 'ai-n4-run1' },
    { nodeId: 'ai-n4', publicHost: 'localhost' },
    { nodeId: 'ai-n4', publicHost: '127.0.0.1' },
    { nodeId: 'ai-n4', servePattern: 'none' },
    { nodeId: 'ai-n4', tailnetIp: LAN_IP },
    { nodeId: 'ai-n4', tailnetMachine: 'x y' },
    { nodeId: 'ai-n4', profile: 'Has Spaces' },
    { nodeId: 'ai-n4', phase: 'enrolled' },
    { nodeId: 'ai-n4', nodeCredentialId: 'x' },
    {},
  ])('refuses %j', (body) => expect(CreateNodeBodySchema.safeParse(body).success).toBe(false));
  it('never echoes a refused tailnet address in its message', () => {
    const bad = v4(5);
    const result = CreateNodeBodySchema.safeParse({ nodeId: 'ai-n4', tailnetIp: bad });
    expect(result.success).toBe(false);
    expect(JSON.stringify(result.error?.issues)).not.toContain(bad);
  });
});

describe('checklist', () => {
  it('has the eight steps in page order', () => {
    expect(CHECKLIST_STEPS).toEqual(['tailnet', 'dns', 'bootstrap_vhost', 'certificate', 'https_vhost', 'enrolled', 'key', 'gateway_test']);
    expect(isChecklistStep('dns')).toBe(true);
    expect(isChecklistStep('publish')).toBe(false);
  });
  it('accepts the three actions only', () => {
    for (const action of ['mark_done', 'mark_todo', 'check']) expect(ChecklistActionBodySchema.safeParse({ action }).success).toBe(true);
    for (const body of [{ action: 'delete' }, {}, { action: 'check', extra: 1 }]) expect(ChecklistActionBodySchema.safeParse(body).success).toBe(false);
  });
});
