// The upstream target builders (docs/specs/gateway.md section 3.5): the table mode x pattern x missing field, the
// resolvers, the exclusion warning (once per node, the node id only) and the stored suffix. Addresses are documentation
// ranges built for the test; no tailnet address appears in this file.
import {
  buildTailnetTarget,
  DEFAULT_TAILNET_PORTS,
  PublicTargets,
  TailnetTargets,
  type TailnetSuffixSource,
  type TargetSubject,
  type UpstreamRoute,
} from './targets';
import { tailnetSuffixFromStored } from './tailnet-suffix';

const SUFFIX = 'example-tailnet.ts.net';
const IP = '203.0.113.7';

const route = (overrides: Partial<UpstreamRoute> = {}): UpstreamRoute => ({ servePattern: 'B', tailnetIp: IP, tailnetMachine: 'node-x', ...overrides });

describe('buildTailnetTarget', () => {
  it('Pattern A: plain http to the address on 18080, no Host or SNI override', () => {
    expect(buildTailnetTarget(route({ servePattern: 'A', tailnetMachine: null }), null)).toEqual({ ok: true, target: { origin: `http://${IP}:18080` } });
  });

  it('Pattern B: https to the address on 443, SNI and Host are <machine>.<suffix>', () => {
    expect(buildTailnetTarget(route(), SUFFIX)).toEqual({
      ok: true,
      target: { origin: `https://${IP}:443`, hostHeader: `node-x.${SUFFIX}`, servername: `node-x.${SUFFIX}` },
    });
  });

  it('an IPv6 address is bracketed', () => {
    expect(buildTailnetTarget(route({ servePattern: 'A', tailnetIp: '2001:db8::1' }), null)).toEqual({ ok: true, target: { origin: 'http://[2001:db8::1]:18080' } });
  });

  it('ports can be replaced (a spec dials a local server)', () => {
    expect(buildTailnetTarget(route({ servePattern: 'A' }), null, { ...DEFAULT_TAILNET_PORTS, a: 4000 })).toEqual({ ok: true, target: { origin: `http://${IP}:4000` } });
  });

  it.each<[string, UpstreamRoute, string | null, string]>([
    ['no address (A)', route({ servePattern: 'A', tailnetIp: null }), null, 'no_tailnet_ip'],
    ['blank address (B)', route({ tailnetIp: '  ' }), SUFFIX, 'no_tailnet_ip'],
    ['a name instead of an address', route({ tailnetIp: 'node-x.example.test' }), SUFFIX, 'invalid_tailnet_ip'],
    ['an address with a zone id', route({ tailnetIp: 'fe80::1%eth0' }), SUFFIX, 'invalid_tailnet_ip'],
    ['an address with a path', route({ tailnetIp: `${IP}/x` }), SUFFIX, 'invalid_tailnet_ip'],
    ['B without a machine', route({ tailnetMachine: null }), SUFFIX, 'no_tailnet_machine'],
    ['B with a machine that is not one DNS label', route({ tailnetMachine: 'a.b' }), SUFFIX, 'invalid_tailnet_machine'],
    ['B with a machine that carries a header break', route({ tailnetMachine: 'a\r\nx' }), SUFFIX, 'invalid_tailnet_machine'],
    ['B without a suffix', route(), null, 'no_tailnet_suffix'],
    ['B with a suffix that is not <tailnet>.ts.net', route(), 'example.test', 'no_tailnet_suffix'],
  ])('refuses: %s', (_name, input, suffix, reason) => {
    expect(buildTailnetTarget(input, suffix)).toEqual({ ok: false, reason });
  });

  it('Pattern A does not need the machine or the suffix', () => {
    expect(buildTailnetTarget(route({ servePattern: 'A', tailnetMachine: null }), null).ok).toBe(true);
  });
});

describe('PublicTargets', () => {
  const subject = (publicHost: string): TargetSubject => ({ nodeId: 'ai-n1', publicHost });

  it('is https://<publicHost>', async () => {
    await expect(new PublicTargets().resolve(subject('ai-n1.example.test'))).resolves.toEqual({ origin: 'https://ai-n1.example.test' });
  });

  it('never dials a host that is not a DNS name, and says which node', async () => {
    const warnings: string[] = [];
    const targets = new PublicTargets(undefined, (line) => warnings.push(line));
    await expect(targets.resolve(subject('evil.test:8080'))).resolves.toBeNull();
    expect(warnings).toEqual(['node ai-n1 has an invalid public host, not calling it']);
  });

  it('is always routable (no routable() at all)', () => {
    expect((new PublicTargets() as { routable?: unknown }).routable).toBeUndefined();
  });
});

describe('TailnetTargets', () => {
  const source = (suffix: string | null): TailnetSuffixSource & { reads: number } => {
    const fake = {
      reads: 0,
      async read() {
        fake.reads += 1;
        return suffix;
      },
    };
    return fake;
  };
  const subject = (nodeId: string, input: UpstreamRoute | undefined): TargetSubject => ({ nodeId, publicHost: `${nodeId}.example.test`, route: input });

  it('resolves a Pattern B node with the suffix setting', async () => {
    const targets = new TailnetTargets({ suffix: source(SUFFIX) });
    await expect(targets.resolve(subject('ai-n1', route()))).resolves.toEqual({ origin: `https://${IP}:443`, hostHeader: `node-x.${SUFFIX}`, servername: `node-x.${SUFFIX}` });
    await expect(targets.routable(subject('ai-n1', route()))).resolves.toBe(true);
  });

  it('does not read the suffix for Pattern A', async () => {
    const suffix = source(null);
    const targets = new TailnetTargets({ suffix });
    await expect(targets.routable(subject('ai-n1', route({ servePattern: 'A' })))).resolves.toBe(true);
    expect(suffix.reads).toBe(0);
  });

  it('an unreadable suffix counts as unset', async () => {
    const targets = new TailnetTargets({ suffix: { read: async () => Promise.reject(new Error('db down')) } });
    await expect(targets.routable(subject('ai-n1', route()))).resolves.toBe(false);
  });

  it('leaves a node out and warns ONCE per node, with the node id and a reason and nothing else', async () => {
    const warnings: string[] = [];
    const targets = new TailnetTargets({ suffix: source(null), warn: (line) => warnings.push(line) });
    const broken = subject('ai-n2', route({ tailnetIp: null }));
    for (let i = 0; i < 3; i += 1) await expect(targets.routable(broken)).resolves.toBe(false);
    await expect(targets.routable(subject('ai-n3', route()))).resolves.toBe(false); // no suffix
    await expect(targets.routable(subject('ai-n3', route()))).resolves.toBe(false);
    expect(warnings).toEqual(['node ai-n2 is left out in tailnet mode: no_tailnet_ip', 'node ai-n3 is left out in tailnet mode: no_tailnet_suffix']);
    expect(warnings.join('\n')).not.toContain(IP);
    expect(warnings.join('\n')).not.toContain(SUFFIX);
  });

  it('warns again when a node that was fixed breaks again', async () => {
    const warnings: string[] = [];
    const targets = new TailnetTargets({ suffix: source(SUFFIX), warn: (line) => warnings.push(line) });
    await targets.routable(subject('ai-n1', route({ tailnetIp: null })));
    await targets.routable(subject('ai-n1', route()));
    await targets.routable(subject('ai-n1', route({ tailnetIp: null })));
    expect(warnings).toHaveLength(2);
  });

  it('a subject without a route is not routable', async () => {
    const targets = new TailnetTargets({ suffix: source(SUFFIX) });
    await expect(targets.resolve(subject('ai-n1', undefined))).resolves.toBeNull();
  });
});

describe('tailnetSuffixFromStored', () => {
  it.each([
    [{ gateway: { tailnetSuffix: SUFFIX } }, SUFFIX],
    [{ 'gateway.tailnetSuffix': SUFFIX }, SUFFIX],
    [{ gateway: { tailnetSuffix: null } }, null],
    [{ gateway: { tailnetSuffix: 'example.test' } }, null],
    [{ gateway: { tailnetSuffix: `x.${SUFFIX}` } }, null],
    [{ gateway: { tailnetSuffix: 7 } }, null],
    [{ gateway: {} }, null],
    [null, null],
    ['text', null],
    [undefined, null],
  ])('%j -> %j', (stored, expected) => expect(tailnetSuffixFromStored(stored)).toBe(expected));
});
