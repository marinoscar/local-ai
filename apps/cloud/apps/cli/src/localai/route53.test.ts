import { randomBytes } from 'node:crypto';

import { describe, expect, it } from 'vitest';

import { Route53Error, Route53HttpClient } from './route53.js';
import { VpsInputError } from './vps-host.js';

const keys = () => ({ accessKeyId: `AKIA${randomBytes(8).toString('hex').toUpperCase()}`, secretAccessKey: randomBytes(30).toString('base64').replace(/[^A-Za-z0-9]/g, 'x') });
const zone = `Z${randomBytes(7).toString('hex').toUpperCase()}`;
const ip = ['203', '0', '113', '10'].join('.');
const now = () => new Date('2026-11-04T12:00:00Z');

function client(responses: Array<{ status: number; body: string }>) {
  const credentials = keys();
  const requests: Array<{ url: string; init: RequestInit }> = [];
  const fetchImpl = (async (url: string, init: RequestInit) => {
    requests.push({ url, init });
    const next = responses.shift() ?? { status: 500, body: '' };
    return new Response(next.body, { status: next.status });
  }) as unknown as typeof fetch;
  return { api: new Route53HttpClient(credentials, { fetch: fetchImpl, now }), credentials, requests };
}

const changeOk = '<ChangeResourceRecordSetsResponse><ChangeInfo><Id>/change/C0123ABC</Id><Status>PENDING</Status></ChangeInfo></ChangeResourceRecordSetsResponse>';
const error = (code: string, message: string) => `<ErrorResponse><Error><Type>Sender</Type><Code>${code}</Code><Message>${message}</Message></Error></ErrorResponse>`;

describe('Route53HttpClient', () => {
  it('sends a signed UPSERT of one A record and returns the change id', async () => {
    const { api, credentials, requests } = client([{ status: 200, body: changeOk }]);
    await expect(api.upsertA(zone, 'ai-n3-staging.marin.cr', ip, 300)).resolves.toEqual({ changeId: 'C0123ABC' });
    const { url, init } = requests[0]!;
    expect(url).toBe(`https://route53.amazonaws.com/2013-04-01/hostedzone/${zone}/rrset/`);
    expect(init.method).toBe('POST');
    const body = String(init.body);
    expect(body).toContain('<Action>UPSERT</Action>');
    expect(body).toContain('<Name>ai-n3-staging.marin.cr.</Name>');
    expect(body).toContain('<TTL>300</TTL>');
    expect(body).toContain(`<Value>${ip}</Value>`);
    const headers = init.headers as Record<string, string>;
    expect(headers.authorization).toMatch(/^AWS4-HMAC-SHA256 Credential=AKIA[0-9A-F]+\/20261104\/us-east-1\/route53\/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=[0-9a-f]{64}$/);
    // The secret key is in no URL, header value or body.
    for (const part of [url, body, ...Object.values(headers)]) expect(part).not.toContain(credentials.secretAccessKey);
  });

  it('sends a DELETE and reports an absent record', async () => {
    const { api, requests } = client([
      { status: 400, body: error('InvalidChangeBatch', "Tried to delete resource record set [name='ai-n3.marin.cr.', type='A'] but it was not found") },
      { status: 200, body: changeOk },
    ]);
    await expect(api.deleteA(zone, 'ai-n3.marin.cr', ip, 300)).resolves.toBe('absent');
    await expect(api.deleteA(zone, 'ai-n3.marin.cr', ip, 300)).resolves.toEqual({ changeId: 'C0123ABC' });
    expect(String(requests[1]!.init.body)).toContain('<Action>DELETE</Action>');
  });

  it('refuses a record whose value does not match, without passing the remote text on', async () => {
    const { api } = client([{ status: 400, body: error('InvalidChangeBatch', `values provided do not match the current values for ${ip}`) }]);
    const caught = await api.deleteA(zone, 'ai-n3.marin.cr', ip, 300).catch((e: unknown) => e);
    expect(caught).toBeInstanceOf(Route53Error);
    expect((caught as Route53Error).code).toBe('RecordValueMismatch');
    expect((caught as Error).message).not.toContain(ip);
  });

  it('keeps only the AWS error code', async () => {
    const { api } = client([{ status: 403, body: error('AccessDenied', 'User arn:aws:iam::123456789012:user/x is not authorized') }]);
    const caught = (await api.upsertA(zone, 'ai-n3.marin.cr', ip, 300).catch((e: unknown) => e)) as Route53Error;
    expect(caught.code).toBe('AccessDenied');
    expect(caught.message).not.toContain('arn:');
  });

  it('reads GetChange', async () => {
    const pending = '<GetChangeResponse><ChangeInfo><Id>/change/C0123ABC</Id><Status>PENDING</Status></ChangeInfo></GetChangeResponse>';
    const insync = pending.replace('PENDING', 'INSYNC');
    const { api, requests } = client([{ status: 200, body: pending }, { status: 200, body: insync }]);
    await expect(api.getChange('C0123ABC')).resolves.toBe('PENDING');
    await expect(api.getChange('C0123ABC')).resolves.toBe('INSYNC');
    expect(requests[0]!.url).toBe('https://route53.amazonaws.com/2013-04-01/change/C0123ABC');
    expect(requests[0]!.init.method).toBe('GET');
  });

  it('turns a network failure into a fixed error', async () => {
    const credentials = keys();
    const boom = (async () => {
      throw new Error(`connect ECONNREFUSED ${ip}:443`);
    }) as unknown as typeof fetch;
    const caught = (await new Route53HttpClient(credentials, { fetch: boom, now }).upsertA(zone, 'ai-n3.marin.cr', ip, 300).catch((e: unknown) => e)) as Route53Error;
    expect(caught.code).toBe('Unreachable');
    expect(caught.message).not.toContain(ip);
  });

  it.each([
    ['a bad zone id', 'zone-1', 'ai-n3.marin.cr', ip, 300],
    ['a host outside the pattern', zone, 'evil.example.test', ip, 300],
    ['a private value', zone, 'ai-n3.marin.cr', '10.0.0.5', 300],
    ['a tailnet value', zone, 'ai-n3.marin.cr', ['100', '100', '1', '1'].join('.'), 300],
    ['a TTL out of range', zone, 'ai-n3.marin.cr', ip, 5],
    ['XML in the host', zone, 'ai-n3.marin.cr<x>', ip, 300],
  ])('refuses %s before any request', async (_label, z, host, value, ttl) => {
    const { api, requests } = client([{ status: 200, body: changeOk }]);
    await expect(api.upsertA(z as string, host as string, value as string, ttl as number)).rejects.toBeInstanceOf(VpsInputError);
    expect(requests).toHaveLength(0);
  });

  it('refuses a bad change id before any request', async () => {
    const { api, requests } = client([]);
    await expect(api.getChange('../x')).rejects.toBeInstanceOf(VpsInputError);
    expect(requests).toHaveLength(0);
  });
});
