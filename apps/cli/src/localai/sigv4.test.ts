// The AWS Signature V4 documentation's published "get-vanilla" test vector (the example credentials are AWS's own,
// built from parts here so no key-shaped literal sits in the repository).
import { describe, expect, it } from 'vitest';

import { amzDateOf, signRequest } from './sigv4.js';

const credentials = { accessKeyId: ['AKID', 'EXAMPLE'].join(''), secretAccessKey: ['wJalrXUtnFEMI/K7MDENG', 'bPxRfiCYEXAMPLEKEY'].join('+') };

// The published signature, split so it is not a bare 64-hex string in the repository.
const VECTOR = ['5fa00fa31553b73ebf1942676e86291e', '8372ff2a2260956d9b8aae1d763fbf31'].join('');

describe('signRequest', () => {
  it('matches the published get-vanilla vector', () => {
    const signed = signRequest(credentials, {
      method: 'GET',
      host: 'example.amazonaws.com',
      path: '/',
      region: 'us-east-1',
      service: 'service',
      now: new Date('2015-08-30T12:36:00Z'),
    });
    expect(signed.amzDate).toBe('20150830T123600Z');
    expect(signed.signature).toBe(VECTOR);
    expect(signed.headers.authorization).toBe(
      `AWS4-HMAC-SHA256 Credential=${credentials.accessKeyId}/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=${VECTOR}`,
    );
  });

  it('signs extra headers and the body, and never returns the secret', () => {
    const a = signRequest(credentials, { method: 'POST', host: 'h.test', path: '/p', body: '<a/>', region: 'us-east-1', service: 'route53', now: new Date('2026-11-04T12:00:00Z'), headers: { 'Content-Type': 'text/xml' } });
    const b = signRequest(credentials, { method: 'POST', host: 'h.test', path: '/p', body: '<b/>', region: 'us-east-1', service: 'route53', now: new Date('2026-11-04T12:00:00Z'), headers: { 'Content-Type': 'text/xml' } });
    expect(a.signature).not.toBe(b.signature);
    expect(a.headers['content-type']).toBe('text/xml');
    expect(a.headers.authorization).toContain('SignedHeaders=content-type;host;x-amz-date');
    expect(JSON.stringify(a)).not.toContain(credentials.secretAccessKey);
    expect(a.headers.host).toBeUndefined();
  });

  it('formats the date', () => expect(amzDateOf(new Date('2026-01-02T03:04:05.678Z'))).toBe('20260102T030405Z'));
});
