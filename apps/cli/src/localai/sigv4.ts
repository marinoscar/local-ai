// AWS Signature Version 4 for one request, with no SDK: the stack-agent calls exactly two Route53 operations, and a
// dependency tree for that would be larger than the code. Pure apart from the clock the caller passes in.
// Reference: the public AWS Signature V4 documentation and test suite.
import { createHash, createHmac } from 'node:crypto';

export interface AwsCredentials {
  accessKeyId: string;
  secretAccessKey: string;
}

export interface SignInput {
  method: 'GET' | 'POST';
  host: string;
  /** The URI path, already percent-encoded as it is sent. */
  path: string;
  /** The canonical query string (sorted, encoded), empty for none. */
  query?: string;
  body?: string;
  region: string;
  service: string;
  now: Date;
  /** Extra headers to sign (lower-case names). `host` and `x-amz-date` are always signed. */
  headers?: Readonly<Record<string, string>>;
}

export interface SignedRequest {
  /** `x-amz-date`, `authorization` and the extra headers; `host` is left to the HTTP client. */
  headers: Record<string, string>;
  amzDate: string;
  signature: string;
}

const sha256Hex = (text: string): string => createHash('sha256').update(text, 'utf8').digest('hex');
const hmac = (key: Buffer | string, text: string): Buffer => createHmac('sha256', key).update(text, 'utf8').digest();

/** `20150830T123600Z`. */
export function amzDateOf(now: Date): string {
  return now.toISOString().replace(/[:-]|\.\d{3}/g, '');
}

export function signRequest(credentials: AwsCredentials, input: SignInput): SignedRequest {
  const amzDate = amzDateOf(input.now);
  const dateStamp = amzDate.slice(0, 8);
  const headers: Record<string, string> = { host: input.host, 'x-amz-date': amzDate };
  for (const [name, value] of Object.entries(input.headers ?? {})) headers[name.toLowerCase()] = value.trim().replace(/\s+/g, ' ');

  const names = Object.keys(headers).sort();
  const canonicalHeaders = names.map((name) => `${name}:${headers[name]}\n`).join('');
  const signedHeaders = names.join(';');
  const canonicalRequest = [input.method, input.path, input.query ?? '', canonicalHeaders, signedHeaders, sha256Hex(input.body ?? '')].join('\n');

  const scope = `${dateStamp}/${input.region}/${input.service}/aws4_request`;
  const stringToSign = ['AWS4-HMAC-SHA256', amzDate, scope, sha256Hex(canonicalRequest)].join('\n');

  const kDate = hmac(`AWS4${credentials.secretAccessKey}`, dateStamp);
  const kRegion = hmac(kDate, input.region);
  const kService = hmac(kRegion, input.service);
  const kSigning = hmac(kService, 'aws4_request');
  const signature = createHmac('sha256', kSigning).update(stringToSign, 'utf8').digest('hex');

  const authorization = `AWS4-HMAC-SHA256 Credential=${credentials.accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`;
  const { host: _host, ...sendable } = headers;
  return { headers: { ...sendable, authorization }, amzDate, signature };
}
