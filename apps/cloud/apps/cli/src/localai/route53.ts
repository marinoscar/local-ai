// The two Route53 operations the stack-agent needs, behind an interface (docs/specs/cloud-app.md section 4.10):
// ChangeResourceRecordSets (UPSERT and DELETE of one A record) and GetChange. The IAM key is restricted to exactly
// those two actions, so there is NO list call: existence is learned from the answer to the change itself.
//
// SECRET HANDLING. The access key lives in this object only for the job. It signs requests in memory; it is never put
// in a URL, a body, a log line, an error message or a returned value. A Route53 error message is never passed on
// (it can echo names and values): only the error CODE, which is a fixed AWS word, is kept.
import { signRequest, type AwsCredentials } from './sigv4.js';
import { CHANGE_ID_RE, HOSTED_ZONE_ID_RE, VPS_HOST_RE, VpsInputError, isPublicIpv4 } from './vps-host.js';

export type ChangeStatus = 'PENDING' | 'INSYNC';

export interface Route53Client {
  /** Creates or replaces the A record of `host`. Idempotent. */
  upsertA(zoneId: string, host: string, ip: string, ttl: number): Promise<{ changeId: string }>;
  /** Deletes the A record. `absent` when the record does not exist. A different current value is a `Route53Error`. */
  deleteA(zoneId: string, host: string, ip: string, ttl: number): Promise<{ changeId: string } | 'absent'>;
  getChange(changeId: string): Promise<ChangeStatus>;
}

/** A refused Route53 call. `code` is the AWS error code (a fixed word) or one of ours; the message never has a remote text. */
export class Route53Error extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'Route53Error';
  }
}

export interface Route53HttpOptions {
  fetch?: typeof globalThis.fetch;
  now?: () => Date;
  /** Test seam. */
  endpointHost?: string;
  timeoutMs?: number;
}

const ENDPOINT_HOST = 'route53.amazonaws.com';
const REGION = 'us-east-1';
const SERVICE = 'route53';
const XMLNS = 'https://route53.amazonaws.com/doc/2013-04-01/';

function changeBatchXml(action: 'UPSERT' | 'DELETE', host: string, ip: string, ttl: number): string {
  return (
    '<?xml version="1.0" encoding="UTF-8"?>' +
    `<ChangeResourceRecordSetsRequest xmlns="${XMLNS}"><ChangeBatch><Comment>local-ai vps stack-agent</Comment><Changes><Change>` +
    `<Action>${action}</Action><ResourceRecordSet><Name>${host}.</Name><Type>A</Type><TTL>${ttl}</TTL>` +
    `<ResourceRecords><ResourceRecord><Value>${ip}</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>` +
    '</Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>'
  );
}

function tag(xml: string, name: string): string | null {
  const match = new RegExp(`<${name}>([^<]{1,200})</${name}>`).exec(xml);
  return match ? match[1]! : null;
}

export class Route53HttpClient implements Route53Client {
  private readonly fetchImpl: typeof globalThis.fetch;
  private readonly now: () => Date;
  private readonly host: string;
  private readonly timeoutMs: number;

  constructor(
    private readonly credentials: AwsCredentials,
    options: Route53HttpOptions = {},
  ) {
    this.fetchImpl = options.fetch ?? globalThis.fetch;
    this.now = options.now ?? (() => new Date());
    this.host = options.endpointHost ?? ENDPOINT_HOST;
    this.timeoutMs = options.timeoutMs ?? 20_000;
  }

  async upsertA(zoneId: string, host: string, ip: string, ttl: number): Promise<{ changeId: string }> {
    const result = await this.change('UPSERT', zoneId, host, ip, ttl);
    if (result === 'absent') throw new Route53Error('Unexpected', 'Route53 reported the record as absent after an UPSERT.');
    return result;
  }

  deleteA(zoneId: string, host: string, ip: string, ttl: number): Promise<{ changeId: string } | 'absent'> {
    return this.change('DELETE', zoneId, host, ip, ttl);
  }

  async getChange(changeId: string): Promise<ChangeStatus> {
    if (!CHANGE_ID_RE.test(changeId)) throw new VpsInputError('The Route53 change id is not valid.');
    const response = await this.send('GET', `/2013-04-01/change/${changeId}`, undefined);
    if (response.status !== 200) throw new Route53Error(tag(response.text, 'Code') ?? `HTTP${response.status}`, 'Route53 refused the status request.');
    const status = tag(response.text, 'Status');
    if (status === 'INSYNC') return 'INSYNC';
    if (status === 'PENDING') return 'PENDING';
    throw new Route53Error('Unexpected', 'Route53 returned a status this agent does not know.');
  }

  private async change(action: 'UPSERT' | 'DELETE', zoneId: string, host: string, ip: string, ttl: number): Promise<{ changeId: string } | 'absent'> {
    // Everything that goes into the XML or the path is checked here, whoever the caller is.
    if (!HOSTED_ZONE_ID_RE.test(zoneId)) throw new VpsInputError('The Route53 hosted zone id is not valid.');
    if (!VPS_HOST_RE.test(host)) throw new VpsInputError('The host name is not one this agent may publish.');
    if (!isPublicIpv4(ip)) throw new VpsInputError('The record value is not a public IPv4 address.');
    if (!Number.isInteger(ttl) || ttl < 60 || ttl > 86_400) throw new VpsInputError('The record TTL is out of range.');

    const response = await this.send('POST', `/2013-04-01/hostedzone/${zoneId}/rrset/`, changeBatchXml(action, host, ip, ttl));
    if (response.status === 200) {
      const id = tag(response.text, 'Id')?.replace(/^\/change\//, '') ?? '';
      if (!CHANGE_ID_RE.test(id)) throw new Route53Error('Unexpected', 'Route53 returned a change id this agent cannot read.');
      return { changeId: id };
    }
    const code = tag(response.text, 'Code') ?? `HTTP${response.status}`;
    if (action === 'DELETE' && code === 'InvalidChangeBatch') {
      const message = tag(response.text, 'Message') ?? '';
      if (/not found/i.test(message)) return 'absent';
      if (/do not match|does not match/i.test(message)) throw new Route53Error('RecordValueMismatch', 'The A record has a different value than this agent created.');
    }
    throw new Route53Error(code.replace(/[^A-Za-z0-9]/g, '').slice(0, 64) || 'Unknown', 'Route53 refused the change.');
  }

  private async send(method: 'GET' | 'POST', path: string, body: string | undefined): Promise<{ status: number; text: string }> {
    const signed = signRequest(this.credentials, {
      method,
      host: this.host,
      path,
      body,
      region: REGION,
      service: SERVICE,
      now: this.now(),
      headers: body === undefined ? {} : { 'content-type': 'text/xml' },
    });
    let response: Response;
    try {
      response = await this.fetchImpl(`https://${this.host}${path}`, {
        method,
        headers: signed.headers,
        body,
        redirect: 'manual',
        signal: AbortSignal.timeout(this.timeoutMs),
      });
    } catch {
      // The cause (a socket error) is not ours to pass on: it can name the endpoint's address.
      throw new Route53Error('Unreachable', 'Route53 could not be reached.');
    }
    const text = (await response.text()).slice(0, 16_384);
    return { status: response.status, text };
  }
}
