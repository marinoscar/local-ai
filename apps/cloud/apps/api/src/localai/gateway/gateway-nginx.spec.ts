// The inner nginx mapping of the public /v1/ (docs/specs/gateway.md section 3.1) and the spec agree, line for line.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const REPO = join(__dirname, '..', '..', '..', '..', '..', '..', '..');
const CONF = readFileSync(join(REPO, 'apps', 'cloud', 'infra', 'nginx', 'app.d', 'locations', 'gateway.conf'), 'utf8');
const SPEC = readFileSync(join(REPO, 'docs', 'specs', 'gateway.md'), 'utf8');

const directives = (text: string): string[] =>
  text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith('#'));

/** The first ```nginx fence after the "Inner nginx" paragraph of section 3.1. */
function specBlock(): string {
  const start = SPEC.indexOf('Inner nginx:');
  const open = SPEC.indexOf('```nginx', start);
  const close = SPEC.indexOf('```', open + 8);
  return SPEC.slice(open + '```nginx'.length, close);
}

describe('gateway.conf', () => {
  it('contains every directive of the spec block (the spec location, verbatim)', () => {
    const wanted = directives(specBlock());
    expect(wanted.length).toBeGreaterThan(15);
    const have = new Set(directives(CONF));
    expect(wanted.filter((line) => !have.has(line))).toEqual([]);
  });

  it('maps /v1/ onto /api/gateway/v1/ unbuffered, with the request id and the real address', () => {
    for (const line of [
      'location /v1/ {',
      'proxy_pass http://api_upstream/api/gateway/v1/;',
      'proxy_buffering off;',
      'proxy_set_header X-Request-ID $request_id;',
      'proxy_set_header X-Real-IP $remote_addr;',
      'proxy_read_timeout 1800s;',
      'client_max_body_size 26m;',
    ]) {
      expect(directives(CONF)).toContain(line);
    }
  });

  it('redirects /v1 to /v1/ with a relative Location', () => {
    expect(CONF).toMatch(/location = \/v1 \{\s*absolute_redirect off;\s*return 301 \/v1\/;\s*\}/);
  });

  it('answers a body over the nginx limit in the gateway error shape (LAI-GW-004)', () => {
    expect(CONF).toContain('error_page 413 @gateway_request_too_large;');
    expect(CONF).toContain('"localai_code":"LAI-GW-004"');
    expect(CONF).toContain('"code":"request_too_large"');
  });
});
