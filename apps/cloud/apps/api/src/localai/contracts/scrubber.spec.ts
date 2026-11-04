// The secret scrubber (docs/specs/cloud-app.md section 3.6 rules; security-model.md section 5 patterns).
// Every secret-looking string is built at test time (test/localai/state-fixtures.ts): none is a literal.
import { ai3, lab3, secrets } from '../../../test/localai/state-fixtures';
import { SCRUB_MAX_DEPTH, scrubSecrets, scrubString, ScrubLimitError } from './scrubber';

describe('scrubString: one pattern at a time', () => {
  const hits: Array<[string, () => string, string]> = [
    ['a lat_ token', () => secrets.labToken(), 'node_token'],
    ['a nod_ token', () => secrets.nodeToken(), 'node_token'],
    ['a pat_ token', () => secrets.patToken(), 'node_token'],
    ['a lai_ token', () => secrets.laiToken(), 'node_token'],
    ['a token inside a sentence', () => `key is ${secrets.labToken()} ok`, 'node_token'],
    ['a 64-hex key', () => secrets.hexKey(), 'hex_key'],
    ['a 64-hex key in upper case', () => secrets.hexKey().toUpperCase(), 'hex_key'],
    ['a tailnet IP (100.101.x.x)', () => secrets.tailnetIp(), 'tailnet_ip'],
    ['the bottom of the tailnet range (100.64.x.x)', () => secrets.tailnetIpLow(), 'tailnet_ip'],
    ['the top of the tailnet range (100.127.x.x)', () => [100, 127, 255, 255].join('.'), 'tailnet_ip'],
    ['a tailnet IP with a port', () => `${secrets.tailnetIp()}:8080`, 'tailnet_ip'],
    ['a tailnet IP in a URL', () => `http://${secrets.tailnetIp()}/v1`, 'tailnet_ip'],
    ['a tailnet IP at the end of a sentence', () => `reach ${secrets.tailnetIp()}.`, 'tailnet_ip'],
    ['a 192.168 address', () => secrets.lanIp(), 'private_ip'],
    ['a 10.x address', () => secrets.privateTen(), 'private_ip'],
    ['a 172.16 address', () => secrets.private172(), 'private_ip'],
    ['the top of 172.16/12 (172.31)', () => [172, 31, 0, 1].join('.'), 'private_ip'],
    ['a path under /home', () => secrets.homePath(), 'home_path'],
    ['a /home path after text', () => `mounted at ${secrets.homePath()}`, 'home_path'],
  ];
  it.each(hits)('refuses %s', (_name, make, rule) => {
    expect(scrubString(make())).toBe(rule);
  });

  const clean: Array<[string, string]> = [
    ['an agent version', '0.4.0'],
    ['a four-part version that is not an address', '10.1.2.3.4'],
    ['a version with a v prefix', 'v110.1.2.3'],
    ['100.63.x.x (just below the tailnet range)', [100, 63, 1, 1].join('.')],
    ['100.128.x.x (just above the tailnet range)', [100, 128, 1, 1].join('.')],
    ['172.15.x.x (just below RFC 1918)', [172, 15, 1, 1].join('.')],
    ['172.32.x.x (just above RFC 1918)', [172, 32, 1, 1].join('.')],
    ['a public address', [8, 8, 8, 8].join('.')],
    ['a localai_ metric name', 'localai_models_loaded_total_by_node'],
    ['a model id', 'qwen3.5-9b-q4km'],
    ['a doctor code', 'LAI-CLD-029'],
    ['a short token-like word', 'lat_short'],
    ['a 63-hex string', 'a'.repeat(63)],
    ['a 65-hex string is one long word, not a 64-hex key', 'a'.repeat(65)],
    ['a path that only mentions home', '/srv/homepage/readme'],
    ['an uuid', '5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f'],
    ['the drill transcript sentence', 'el veloz murciélago hindú comía feliz cardillo y kiwi'],
    ['the empty string', ''],
  ];
  it.each(clean)('passes %s', (_name, text) => {
    expect(scrubString(text)).toBeNull();
  });
});

describe('scrubSecrets: the document walk', () => {
  it('passes the full ai-n3 fixture and every snapshot of the 3-node lab', () => {
    expect(scrubSecrets(ai3())).toBeNull();
    for (const snapshot of lab3()) expect(scrubSecrets(snapshot)).toBeNull();
  });

  it('passes scalars and empty containers', () => {
    expect(scrubSecrets(null)).toBeNull();
    expect(scrubSecrets(42)).toBeNull();
    expect(scrubSecrets({})).toBeNull();
    expect(scrubSecrets([])).toBeNull();
  });

  it('reports the JSON path of a hit in a nested object', () => {
    const snapshot = ai3();
    snapshot.llama.build = secrets.tailnetIp();
    expect(scrubSecrets(snapshot)).toEqual({ path: 'llama.build', rule: 'tailnet_ip' });
  });

  it('reports array indexes in the path', () => {
    const snapshot = ai3();
    snapshot.models[3]!.loadable_reason = `needs ${secrets.nodeToken()}`;
    expect(scrubSecrets(snapshot)).toEqual({ path: 'models[3].loadable_reason', rule: 'node_token' });
  });

  it('finds a hit deep in nested arrays of objects (ark.roots[].ready.items[].reason)', () => {
    const snapshot = ai3();
    snapshot.ark.roots[0]!.ready!.items[2]!.reason = secrets.homePath();
    expect(scrubSecrets(snapshot)).toEqual({ path: 'ark.roots[0].ready.items[2].reason', rule: 'home_path' });
  });

  it('finds a hit in a drill check detail value', () => {
    const snapshot = ai3();
    snapshot.drill!.results!.checks[0]!.details = { answer: secrets.lanIp() };
    expect(scrubSecrets(snapshot)).toEqual({ path: 'drill.results.checks[0].details.answer', rule: 'private_ip' });
  });

  it('refuses a secret-looking object KEY and reports it as <key>, never the key itself', () => {
    const snapshot = ai3();
    snapshot.drill!.results!.checks[0]!.details = { [secrets.hexKey()]: 'x' };
    const finding = scrubSecrets(snapshot);
    expect(finding).toEqual({ path: 'drill.results.checks[0].details.<key>', rule: 'hex_key' });
    expect(JSON.stringify(finding)).not.toContain(secrets.hexKey());
  });

  it('never puts a free-text key or any value into the reported path', () => {
    const value = secrets.labToken();
    const finding = scrubSecrets({ 'free text key': { inner: value } });
    expect(finding).toEqual({ path: '<key>.inner', rule: 'node_token' });
    expect(JSON.stringify(finding)).not.toContain(value);
  });

  it('a hit at the root is reported as $', () => {
    expect(scrubSecrets(secrets.tailnetIp())).toEqual({ path: '$', rule: 'tailnet_ip' });
  });

  it('numbers, booleans and nulls are never findings', () => {
    expect(scrubSecrets({ a: 100, b: true, c: null, d: [1, 2, 3] })).toBeNull();
  });

  it('refuses a body nested deeper than the cap without overflowing the stack', () => {
    let deep: unknown = 'x';
    for (let i = 0; i < 100_000; i += 1) deep = [deep];
    expect(() => scrubSecrets(deep)).toThrow(ScrubLimitError);
    let atCap: unknown = 'x';
    for (let i = 0; i < SCRUB_MAX_DEPTH; i += 1) atCap = [atCap];
    expect(scrubSecrets(atCap)).toBeNull();
  });
});
