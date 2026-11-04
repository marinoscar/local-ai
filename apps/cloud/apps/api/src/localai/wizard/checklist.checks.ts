// The automatic checks of the publish checklist (docs/specs/cloud-app.md section 4.8). Each takes the `NetworkProbe`
// and the facts it needs and returns `{ state, detail }`.
//
// `detail` is plain text for the operator and is stored and audited, so it is built here from FIXED sentences plus
// numbers and names the operator already typed (the host, a model id from the node's own snapshot). It never carries
// an IP address, a response body, a header, a key or the text of an error thrown by the probe: every probe failure
// becomes one of the sentences below.
import { CHECK_TIMEOUT_MS, type NetworkProbe } from './network-probe.port';

export interface CheckResult {
  state: 'done' | 'failed';
  detail: string;
}

const done = (detail: string): CheckResult => ({ state: 'done', detail });
const failed = (detail: string): CheckResult => ({ state: 'failed', detail });

const MS_PER_DAY = 86_400_000;
/** A certificate with fewer days left than this fails (spec section 4.8). */
export const MIN_CERT_DAYS = 7;

export const TIMED_OUT = Symbol('timed out');

/** Runs `work` but gives up after `timeoutMs`, whatever the probe does. The probe's own timeout is a second line. */
export async function within<T>(work: Promise<T>, timeoutMs: number): Promise<T | typeof TIMED_OUT> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<typeof TIMED_OUT>((resolve) => {
    timer = setTimeout(() => resolve(TIMED_OUT), timeoutMs);
  });
  try {
    return await Promise.race([work, timeout]);
  } finally {
    if (timer) clearTimeout(timer);
    // If the timeout won, the work may still reject later: that must not become an unhandled rejection.
    work.catch(() => undefined);
  }
}
/** The plain-language reason a call did not finish. */
function unreachable(host: string, what: string, outcome: 'timeout' | 'error'): CheckResult {
  return failed(
    outcome === 'timeout'
      ? `${what} did not answer within ${CHECK_TIMEOUT_MS / 1000} seconds. Check that ${host} is reachable, then press Check again.`
      : `Could not reach ${host} (${what}). Check the previous steps, then press Check again.`,
  );
}

/** `dns.resolve4(publicHost)` must equal `dns.resolve4(vpsHostname)`. */
export async function checkDns(probe: NetworkProbe, publicHost: string, vpsHostname: string, timeoutMs = CHECK_TIMEOUT_MS): Promise<CheckResult> {
  const lookup = async (host: string): Promise<string[] | 'none' | 'timeout'> => {
    try {
      const answer = await within(probe.resolve4(host, { timeoutMs }), timeoutMs);
      if (answer === TIMED_OUT) return 'timeout';
      return [...new Set(answer)].sort();
    } catch {
      return 'none';
    }
  };
  const [own, vps] = await Promise.all([lookup(publicHost), lookup(vpsHostname)]);
  if (own === 'timeout' || vps === 'timeout') return unreachable('the DNS server', 'DNS', 'timeout');
  if (vps === 'none' || vps.length === 0) return failed(`The cloud could not look up ${vpsHostname}, so it cannot compare. Check the "VPS public hostname" setting.`);
  if (own === 'none' || own.length === 0) return failed(`No A record for ${publicHost} yet. Create it in Route 53 (it can take a few minutes to appear), then press Check again.`);
  const same = own.length === vps.length && own.every((address, index) => address === vps[index]);
  return same
    ? done(`${publicHost} points to the same address as ${vpsHostname}.`)
    : failed(`${publicHost} does not point to the same address as ${vpsHostname}. Fix the A record in Route 53 (value: the VPS address), wait a few minutes and press Check again.`);
}

/** A valid Let's Encrypt certificate for the host with at least 7 days left. */
export async function checkCertificate(probe: NetworkProbe, publicHost: string, now: Date, timeoutMs = CHECK_TIMEOUT_MS): Promise<CheckResult> {
  let info;
  try {
    info = await within(probe.tlsCheck(publicHost, 443, { timeoutMs }), timeoutMs);
  } catch {
    return unreachable(publicHost, 'HTTPS on port 443', 'error');
  }
  if (info === TIMED_OUT) return unreachable(publicHost, 'HTTPS on port 443', 'timeout');
  const daysLeft = Math.floor((info.validTo.getTime() - now.getTime()) / MS_PER_DAY);
  if (!info.authorized) return failed(`The server answered, but its certificate is not valid for ${publicHost}. Issue the certificate (certbot) and make sure the final vhost uses it.`);
  if (!/let'?s encrypt/i.test(info.issuerOrganization ?? '')) {
    return failed(`The certificate was not issued by Let's Encrypt${info.issuerOrganization ? ` (issuer: ${info.issuerOrganization})` : ''}. Issue the certificate with the certbot command.`);
  }
  if (daysLeft < MIN_CERT_DAYS) {
    return failed(`The certificate expires in ${Math.max(daysLeft, 0)} day${daysLeft === 1 ? '' : 's'} (at least ${MIN_CERT_DAYS} are needed). Renew it with certbot, then press Check again.`);
  }
  return done(`Valid Let's Encrypt certificate, ${daysLeft} days left.`);
}

/** `/health` answers 200 and `/v1/models` without a key answers 401. */
export async function checkHttpsVhost(probe: NetworkProbe, publicHost: string, timeoutMs = CHECK_TIMEOUT_MS): Promise<CheckResult> {
  const get = async (path: string) => {
    try {
      return await within(probe.httpGet(`https://${publicHost}${path}`, { timeoutMs }), timeoutMs);
    } catch {
      return null;
    }
  };
  const health = await get('/health');
  if (health === TIMED_OUT) return unreachable(publicHost, '/health', 'timeout');
  if (health === null) return unreachable(publicHost, '/health', 'error');
  if (health.status !== 200) {
    return failed(`https://${publicHost}/health answered ${health.status}, not 200. ${health.status === 502 ? 'The vhost cannot reach the node: check the tailnet address and that tailscale serve is on.' : 'Check the final vhost and that nginx was reloaded.'}`);
  }
  const models = await get('/v1/models');
  if (models === TIMED_OUT) return unreachable(publicHost, '/v1/models', 'timeout');
  if (models === null) return unreachable(publicHost, '/v1/models', 'error');
  if (models.status === 200) {
    return failed(`DANGER: https://${publicHost}/v1/models answered 200 without a key. The node is open to the world. Take the vhost out of conf.d and reload nginx at once, then run "aictl doctor" on the node.`);
  }
  if (models.status !== 401) return failed(`https://${publicHost}/v1/models answered ${models.status} without a key; it should answer 401. Check the vhost keeps the Authorization header.`);
  return done('/health answers 200 and /v1/models refuses a request without a key (401).');
}

/** Model ids in an OpenAI-style `/v1/models` body (`{ data: [{ id }] }`). Anything else is an empty list. */
export function modelIdsOf(body: string): string[] {
  try {
    const parsed: unknown = JSON.parse(body);
    const list = (parsed as { data?: unknown })?.data;
    if (!Array.isArray(list)) return [];
    return list.flatMap((entry) => (entry && typeof (entry as { id?: unknown }).id === 'string' ? [(entry as { id: string }).id] : []));
  } catch {
    return [];
  }
}

export type GatewayKey = { kind: 'key'; key: string } | { kind: 'missing' } | { kind: 'unreadable' };

/** `/v1/models` with the node's stored key answers 200 and lists the models the snapshot says are loaded. */
export async function checkGateway(
  probe: NetworkProbe,
  publicHost: string,
  key: GatewayKey,
  loadedModelIds: readonly string[],
  timeoutMs = CHECK_TIMEOUT_MS,
): Promise<CheckResult> {
  if (key.kind === 'missing') return failed('The cloud has no API key for this node yet (the key step must be done first). On the node run "sudo aictl cloud resend-key".');
  if (key.kind === 'unreadable') return failed('The cloud cannot read this node’s stored key (LAI-CLD-032). Restore SECRETS_ENCRYPTION_KEY, then ask all nodes to re-send their keys.');
  let response;
  try {
    // The key goes into this one header and nowhere else; the probe is told never to echo it.
    response = await within(probe.httpGet(`https://${publicHost}/v1/models`, { timeoutMs, headers: { Authorization: `Bearer ${key.key}` } }), timeoutMs);
  } catch {
    return unreachable(publicHost, '/v1/models', 'error');
  }
  if (response === TIMED_OUT) return unreachable(publicHost, '/v1/models', 'timeout');
  if (response.status === 401 || response.status === 403) return failed('The node refused the key the cloud holds. On the node run "sudo aictl cloud resend-key" so both agree.');
  if (response.status !== 200) return failed(`https://${publicHost}/v1/models answered ${response.status} with the key; it should answer 200.`);
  const listed = new Set(modelIdsOf(response.body));
  const missing = loadedModelIds.filter((id) => !listed.has(id));
  if (missing.length > 0) return failed(`The node answered 200, but these loaded models are not in its list: ${missing.slice(0, 5).join(', ')}.`);
  return done(loadedModelIds.length > 0 ? `The gateway path works: 200 with the key, and ${loadedModelIds.length} loaded model${loadedModelIds.length === 1 ? '' : 's'} listed.` : 'The gateway path works: 200 with the key (no model is loaded right now).');
}
