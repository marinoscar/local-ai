// The automatic checks against a FAKE NetworkProbe: every passing and failing variant of the spec (section 4.8) and the
// issue's acceptance list, with the plain detail each gives. Nothing here touches the network.
import { checkCertificate, checkDns, checkGateway, checkHttpsVhost, modelIdsOf, within, TIMED_OUT } from './checklist.checks';
import type { HttpProbeOptions, HttpProbeResponse, NetworkProbe, TlsCertInfo } from './network-probe.port';

const NOW = new Date('2026-11-04T10:00:00.000Z');
const daysFromNow = (days: number) => new Date(NOW.getTime() + days * 86_400_000);
// Addresses from parts: no literal address in the repository.
const addr = (last: number) => ['198', '51', '100', last].join('.');

interface FakeConfig {
  dns?: Record<string, string[] | Error>;
  tls?: TlsCertInfo | Error;
  http?: Record<string, HttpProbeResponse | Error>;
  hang?: boolean;
}

class FakeProbe implements NetworkProbe {
  readonly calls: Array<{ kind: string; target: string; headers?: Readonly<Record<string, string>> }> = [];
  constructor(private readonly config: FakeConfig) {}
  async resolve4(host: string): Promise<string[]> {
    this.calls.push({ kind: 'dns', target: host });
    if (this.config.hang) return new Promise(() => undefined);
    const answer = this.config.dns?.[host] ?? new Error('ENOTFOUND');
    if (answer instanceof Error) throw answer;
    return answer;
  }
  async tlsCheck(host: string): Promise<TlsCertInfo> {
    this.calls.push({ kind: 'tls', target: host });
    if (this.config.hang) return new Promise(() => undefined);
    if (!this.config.tls || this.config.tls instanceof Error) throw this.config.tls ?? new Error('ECONNREFUSED');
    return this.config.tls;
  }
  async httpGet(url: string, options: HttpProbeOptions): Promise<HttpProbeResponse> {
    this.calls.push({ kind: 'http', target: url, headers: options.headers });
    if (this.config.hang) return new Promise(() => undefined);
    const answer = this.config.http?.[url] ?? new Error('ECONNREFUSED');
    if (answer instanceof Error) throw answer;
    return answer;
  }
}

const cert = (patch: Partial<TlsCertInfo> = {}): TlsCertInfo => ({ authorized: true, issuerOrganization: "Let's Encrypt", issuerCommonName: 'R11', validTo: daysFromNow(60), ...patch });
const ok = (body = ''): HttpProbeResponse => ({ status: 200, body });
const status = (code: number): HttpProbeResponse => ({ status: code, body: '' });

describe('checkDns', () => {
  const HOST = 'ai-n4.marin.cr';
  const VPS = 'ai.marin.cr';

  it('passes when the node host resolves to the VPS address (order and duplicates do not matter)', async () => {
    const probe = new FakeProbe({ dns: { [HOST]: [addr(2), addr(1), addr(1)], [VPS]: [addr(1), addr(2)] } });
    const result = await checkDns(probe, HOST, VPS, 50);
    expect(result).toEqual({ state: 'done', detail: `${HOST} points to the same address as ${VPS}.` });
  });

  it('fails with a plain detail for a wrong A record, and does not print an address', async () => {
    const probe = new FakeProbe({ dns: { [HOST]: [addr(9)], [VPS]: [addr(1)] } });
    const result = await checkDns(probe, HOST, VPS, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toContain('does not point to the same address');
    expect(result.detail).not.toContain(addr(9));
    expect(result.detail).not.toContain(addr(1));
  });

  it('fails when the host has no record yet', async () => {
    const result = await checkDns(new FakeProbe({ dns: { [VPS]: [addr(1)] } }), HOST, VPS, 50);
    expect(result).toMatchObject({ state: 'failed' });
    expect(result.detail).toMatch(/No A record for ai-n4\.marin\.cr/);
  });

  it('fails, naming the setting, when the VPS name itself cannot be looked up', async () => {
    const result = await checkDns(new FakeProbe({ dns: { [HOST]: [addr(1)] } }), HOST, VPS, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/VPS public hostname/);
  });

  it('gives up after the timeout when the resolver hangs', async () => {
    const result = await checkDns(new FakeProbe({ hang: true }), HOST, VPS, 20);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/did not answer within 10 seconds/);
  });
});

describe('checkCertificate', () => {
  const HOST = 'ai-n4.marin.cr';

  it('passes for a valid Let’s Encrypt certificate with at least 7 days left', async () => {
    expect(await checkCertificate(new FakeProbe({ tls: cert({ validTo: daysFromNow(60) }) }), HOST, NOW, 50)).toEqual({ state: 'done', detail: "Valid Let's Encrypt certificate, 60 days left." });
    expect((await checkCertificate(new FakeProbe({ tls: cert({ validTo: daysFromNow(7.2) }) }), HOST, NOW, 50)).state).toBe('done');
  });

  it('fails when the certificate expires in less than 7 days, saying how many', async () => {
    const result = await checkCertificate(new FakeProbe({ tls: cert({ validTo: daysFromNow(3.5) }) }), HOST, NOW, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/expires in 3 days \(at least 7 are needed\)/);
  });

  it('fails for an expired certificate without a negative count', async () => {
    const result = await checkCertificate(new FakeProbe({ tls: cert({ validTo: daysFromNow(-2) }) }), HOST, NOW, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/expires in 0 days/);
  });

  it('fails when the chain does not verify for the name', async () => {
    const result = await checkCertificate(new FakeProbe({ tls: cert({ authorized: false }) }), HOST, NOW, 50);
    expect(result).toMatchObject({ state: 'failed' });
    expect(result.detail).toMatch(/not valid for ai-n4\.marin\.cr/);
  });

  it('fails for another issuer, naming it', async () => {
    const result = await checkCertificate(new FakeProbe({ tls: cert({ issuerOrganization: 'Org Alpha CA' }) }), HOST, NOW, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/not issued by Let's Encrypt \(issuer: Org Alpha CA\)/);
  });

  it('fails plainly when nothing answers on 443, and never repeats the probe error text', async () => {
    const result = await checkCertificate(new FakeProbe({ tls: new Error('connect ECONNREFUSED ' + addr(7) + ':443') }), HOST, NOW, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).not.toContain('ECONNREFUSED');
    expect(result.detail).not.toContain(addr(7));
  });

  it('times out', async () => {
    expect((await checkCertificate(new FakeProbe({ hang: true }), HOST, NOW, 20)).detail).toMatch(/did not answer within 10 seconds/);
  });
});

describe('checkHttpsVhost', () => {
  const HOST = 'ai-n4.marin.cr';
  const health = `https://${HOST}/health`;
  const models = `https://${HOST}/v1/models`;

  it('passes when /health is 200 and /v1/models without a key is 401', async () => {
    const probe = new FakeProbe({ http: { [health]: ok('{"status":"ok"}'), [models]: status(401) } });
    expect(await checkHttpsVhost(probe, HOST, 50)).toMatchObject({ state: 'done' });
    // The two requests carry no Authorization header: that is the point of the second one.
    expect(probe.calls.filter((call) => call.kind === 'http').every((call) => call.headers === undefined)).toBe(true);
  });

  it('fails LOUDLY when /v1/models answers 200 without a key (the node is open to the world)', async () => {
    const result = await checkHttpsVhost(new FakeProbe({ http: { [health]: ok(), [models]: ok('{"data":[]}') } }), HOST, 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/^DANGER: /);
    expect(result.detail).toMatch(/open to the world/);
  });

  it('fails with the status when /health is not 200, and explains a 502', async () => {
    const bad = await checkHttpsVhost(new FakeProbe({ http: { [health]: status(404) } }), HOST, 50);
    expect(bad).toMatchObject({ state: 'failed' });
    expect(bad.detail).toContain('answered 404');
    expect((await checkHttpsVhost(new FakeProbe({ http: { [health]: status(502) } }), HOST, 50)).detail).toMatch(/cannot reach the node/);
  });

  it('fails when /v1/models answers something other than 401 without a key', async () => {
    expect((await checkHttpsVhost(new FakeProbe({ http: { [health]: ok(), [models]: status(404) } }), HOST, 50)).detail).toContain('answered 404 without a key');
  });

  it('fails plainly when the host cannot be reached, and times out', async () => {
    expect((await checkHttpsVhost(new FakeProbe({}), HOST, 50)).detail).toMatch(/Could not reach/);
    expect((await checkHttpsVhost(new FakeProbe({ hang: true }), HOST, 20)).detail).toMatch(/did not answer within 10 seconds/);
  });
});

describe('checkGateway', () => {
  const HOST = 'ai-n4.marin.cr';
  const models = `https://${HOST}/v1/models`;
  // A throwaway key made at run time: no key literal in the repository.
  const KEY = Array.from({ length: 64 }, (_, i) => 'abcdef0123456789'[(i * 7 + 3) % 16]).join('');
  const list = (...ids: string[]) => ok(JSON.stringify({ object: 'list', data: ids.map((id) => ({ id })) }));

  it('sends the key as a bearer header, and passes when 200 lists every loaded model', async () => {
    const probe = new FakeProbe({ http: { [models]: list('model-one', 'model-two', 'model-three') } });
    const result = await checkGateway(probe, HOST, { kind: 'key', key: KEY }, ['model-one', 'model-two'], 50);
    expect(result).toMatchObject({ state: 'done' });
    expect(result.detail).toContain('2 loaded models');
    expect(probe.calls[0]!.headers).toEqual({ Authorization: `Bearer ${KEY}` });
    expect(JSON.stringify(result)).not.toContain(KEY);
  });

  it('passes with 200 when nothing is loaded, and says so', async () => {
    const result = await checkGateway(new FakeProbe({ http: { [models]: list() } }), HOST, { kind: 'key', key: KEY }, [], 50);
    expect(result).toMatchObject({ state: 'done' });
    expect(result.detail).toMatch(/no model is loaded/);
  });

  it('fails, naming the models, when a loaded model is not listed', async () => {
    const result = await checkGateway(new FakeProbe({ http: { [models]: list('model-one') } }), HOST, { kind: 'key', key: KEY }, ['model-one', 'model-two'], 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toContain('model-two');
    expect(result.detail).not.toContain('model-one,');
  });

  it('fails when the node refuses the key the cloud holds, without printing it', async () => {
    const result = await checkGateway(new FakeProbe({ http: { [models]: status(401) } }), HOST, { kind: 'key', key: KEY }, [], 50);
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/refused the key/);
    expect(JSON.stringify(result)).not.toContain(KEY);
  });

  it('fails when the status is not 200', async () => {
    expect((await checkGateway(new FakeProbe({ http: { [models]: status(502) } }), HOST, { kind: 'key', key: KEY }, [], 50)).detail).toContain('answered 502');
  });

  it('does not call the node at all when the cloud has no key, or cannot read it', async () => {
    const probe = new FakeProbe({});
    const missing = await checkGateway(probe, HOST, { kind: 'missing' }, [], 50);
    const unreadable = await checkGateway(probe, HOST, { kind: 'unreadable' }, [], 50);
    expect(missing.state).toBe('failed');
    expect(missing.detail).toMatch(/no API key for this node/);
    expect(unreadable.detail).toContain('LAI-CLD-032');
    expect(probe.calls).toEqual([]);
  });

  it('a transport failure never repeats the probe error (which could echo a header)', async () => {
    const result = await checkGateway(new FakeProbe({ http: { [models]: new Error(`failed with Authorization: Bearer ${KEY}`) } }), HOST, { kind: 'key', key: KEY }, [], 50);
    expect(result.state).toBe('failed');
    expect(JSON.stringify(result)).not.toContain(KEY);
    expect(result.detail).not.toContain('Authorization');
  });

  it('times out', async () => {
    expect((await checkGateway(new FakeProbe({ hang: true }), HOST, { kind: 'key', key: KEY }, [], 20)).detail).toMatch(/did not answer within 10 seconds/);
  });
});

describe('modelIdsOf and within', () => {
  it('reads OpenAI-style lists and treats anything else as empty', () => {
    expect(modelIdsOf('{"data":[{"id":"a"},{"id":"b"},{"x":1},null,{"id":3}]}')).toEqual(['a', 'b']);
    for (const junk of ['', 'not json', '{}', '{"data":"x"}', '[]', 'null']) expect(modelIdsOf(junk)).toEqual([]);
  });
  it('within resolves with the work, or the sentinel when it is too slow, and swallows a late rejection', async () => {
    expect(await within(Promise.resolve(5), 50)).toBe(5);
    expect(await within(new Promise((_resolve, reject) => setTimeout(() => reject(new Error('late')), 30)), 5)).toBe(TIMED_OUT);
    await new Promise((resolve) => setTimeout(resolve, 60));
  });
});
