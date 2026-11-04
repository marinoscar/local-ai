// The VPS stack-agent's two executors (docs/specs/cloud-app.md section 4.10): `localai.vps.publish_node` and
// `localai.vps.unpublish_node`. They run on the VPS host itself (`laictl node start --types …`), claim the two
// node-eligible job types and edit the shared proxy.
//
// RULES THIS FILE KEEPS
//  - Every subprocess is `execFile` with a fixed argument array (vps-process.ts); no shell, no interpolated command.
//  - The host is recomputed from the job's `nodeId` and the staging suffix and checked against the host regex BEFORE
//    any use; the tailnet address and name are validated before they are written into an nginx file.
//  - The Route53 key, the tailnet address and name arrive ONLY through the job's brokered secret (`ctx.api.jobSecret`).
//    They are never logged, never put in a detail, an error or the result; every detail is a fixed sentence that goes
//    through `scrubDetail` anyway.
//  - Every step is idempotent and journalled. A failed step ends the run: the result carries the journal up to and
//    including it (status `failed`), so the cloud can show what happened; later steps do not run.
//  - A failed `nginx -t` restores the previous vhost and reloads (nginx.ts); the proxy is never left un-reloadable.
import type { JobExecutionContext, JobExecutor } from '@marinoscar/platform-cli/node';

import { CertbotRefusedError, Certbot } from './certbot.js';
import { NginxProxy, NginxRefusedError } from './nginx.js';
import { Route53Error, Route53HttpClient, type Route53Client } from './route53.js';
import {
  DNS_NAME_RE,
  EMAIL_RE,
  HOSTED_ZONE_ID_RE,
  VpsInputError,
  isAllowedVpsHost,
  isNodeId,
  isStagingSuffix,
  isTailnetAddress,
  TAILNET_NAME_RE,
} from './vps-host.js';
import { DigChecker, allResolversAnswer, vpsPublicAddress, type DnsChecker } from './vps-dns.js';
import { FetchHttpProbe, type HttpProbe } from './vps-http.js';
import { ExecFileRunner, type CommandRunner } from './vps-process.js';
import { scrubDetail } from './vps-scrub.js';
import { renderBootstrapVhost, renderFinalVhost } from './vps-templates.js';
import {
  VPS_PUBLISH_TYPE,
  VPS_SECRET_KIND,
  VPS_UNPUBLISH_TYPE,
  type JournalStep,
  type PublishResult,
  type PublishStepName,
  type StepStatus,
  type UnpublishResult,
  type UnpublishStepName,
  type VpsSecret,
} from './vps-types.js';

export { VPS_PUBLISH_TYPE, VPS_UNPUBLISH_TYPE };

const TTL = 300;
const ACCESS_KEY_ID_RE = /^(?:AKIA|ASIA)[A-Z0-9]{12,32}$/;
const SECRET_KEY_RE = /^[A-Za-z0-9/+=]{30,80}$/;

/** Everything the executors touch outside this process, so a test replaces all of it. */
export interface VpsDeps {
  dns: DnsChecker;
  http: HttpProbe;
  route53(credentials: { accessKeyId: string; secretAccessKey: string }): Route53Client;
  nginx: NginxProxy;
  /** Built on first use: only a publish needs the certificate contact. */
  certbot(): Certbot;
  sleep(ms: number): Promise<void>;
  now(): Date;
  timing: { changeWaitMs: number; dnsWaitMs: number; pollMs: number; healthTries: number; healthPauseMs: number };
}

const sleepReal = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** The real dependencies, configured by the worker's own environment (documented in runbook 10). */
export function defaultVpsDeps(env: NodeJS.ProcessEnv = process.env, runner: CommandRunner = new ExecFileRunner()): VpsDeps {
  const proxyDir = env.LOCALAI_VPS_PROXY_DIR?.trim() || '/opt/infra/proxy';
  const container = env.LOCALAI_VPS_PROXY_CONTAINER?.trim() || 'proxy-nginx';
  return {
    dns: new DigChecker(runner),
    http: new FetchHttpProbe(),
    route53: (credentials) => new Route53HttpClient(credentials),
    nginx: new NginxProxy({ runner, proxyDir, container }),
    certbot: () => {
      const email = env.LOCALAI_VPS_CERTBOT_EMAIL?.trim() ?? '';
      if (!EMAIL_RE.test(email)) throw new VpsInputError('LOCALAI_VPS_CERTBOT_EMAIL is not set on the stack-agent.');
      return new Certbot({ runner, proxyDir, email, image: env.LOCALAI_VPS_CERTBOT_IMAGE?.trim() || undefined });
    },
    sleep: sleepReal,
    now: () => new Date(),
    timing: { changeWaitMs: 180_000, dnsWaitMs: 180_000, pollMs: 5_000, healthTries: 4, healthPauseMs: 3_000 },
  };
}

// ------------------------------------------------------------------------------------------------------------------
// Parameters and the brokered secret
// ------------------------------------------------------------------------------------------------------------------

/** `{ nodeId }` and nothing else. */
export function parseParams(params: unknown): { nodeId: string } {
  if (typeof params !== 'object' || params === null || Array.isArray(params)) throw new VpsInputError('The job parameters are not an object.');
  const keys = Object.keys(params);
  if (keys.length !== 1 || keys[0] !== 'nodeId') throw new VpsInputError('The job parameters must be exactly { nodeId }.');
  const nodeId = (params as Record<string, unknown>).nodeId;
  if (!isNodeId(nodeId)) throw new VpsInputError('The node id is not a lab node id (ai-n followed by 1 to 9999).');
  return { nodeId };
}

const asString = (value: unknown): string | null => (typeof value === 'string' ? value : null);

/** Validates the brokered material. Messages are fixed: they never echo a value. */
export function parseSecret(material: unknown, nodeId: string, action: 'publish' | 'unpublish'): VpsSecret {
  const record = typeof material === 'object' && material !== null ? (material as Record<string, unknown>) : null;
  if (record === null) throw new VpsInputError('The brokered credential is not an object.');
  const accessKeyId = asString(record.route53AccessKeyId);
  const secretAccessKey = asString(record.route53SecretAccessKey);
  const hostedZoneId = asString(record.hostedZoneId);
  const host = asString(record.host);
  const stagingSuffix = asString(record.stagingSuffix);
  const vpsPublicHostname = asString(record.vpsPublicHostname);
  if (accessKeyId === null || !ACCESS_KEY_ID_RE.test(accessKeyId) || secretAccessKey === null || !SECRET_KEY_RE.test(secretAccessKey)) {
    throw new VpsInputError('The brokered Route53 key is missing or malformed.');
  }
  if (hostedZoneId === null || !HOSTED_ZONE_ID_RE.test(hostedZoneId)) throw new VpsInputError('The brokered Route53 hosted zone id is missing or malformed.');
  if (stagingSuffix === null || !isStagingSuffix(stagingSuffix)) throw new VpsInputError('The brokered staging suffix is not valid.');
  if (!isAllowedVpsHost(host, nodeId, stagingSuffix)) throw new VpsInputError('The brokered host is not the host of this node under the configured staging suffix.');
  if (vpsPublicHostname === null || !DNS_NAME_RE.test(vpsPublicHostname)) throw new VpsInputError('The brokered VPS public hostname is not a DNS name.');

  const secret: VpsSecret = { route53AccessKeyId: accessKeyId, route53SecretAccessKey: secretAccessKey, hostedZoneId, host, stagingSuffix, vpsPublicHostname };
  if (action === 'publish') {
    const servePattern = record.servePattern;
    const tailnetIp = asString(record.tailnetIp);
    const tailnetMachine = asString(record.tailnetMachine);
    if (servePattern !== 'A' && servePattern !== 'B') throw new VpsInputError('The brokered serve pattern must be A or B.');
    if (tailnetIp === null || !isTailnetAddress(tailnetIp)) throw new VpsInputError('The brokered tailnet address is missing or not a tailnet address.');
    if (tailnetMachine === null || !TAILNET_NAME_RE.test(tailnetMachine)) throw new VpsInputError('The brokered tailnet machine name is missing or malformed.');
    if (servePattern === 'B' && !tailnetMachine.includes('.')) throw new VpsInputError('A Pattern B vhost needs the full tailnet name of the node.');
    secret.servePattern = servePattern;
    secret.tailnetIp = tailnetIp;
    secret.tailnetMachine = tailnetMachine;
  }
  return secret;
}

// ------------------------------------------------------------------------------------------------------------------
// The journal
// ------------------------------------------------------------------------------------------------------------------

class Journal<Name extends string> {
  readonly steps: Array<JournalStep<Name>> = [];
  failed = false;

  add(name: Name, status: StepStatus, detail: string): void {
    this.steps.push({ name, status, detail: scrubDetail(detail) });
    if (status === 'failed') this.failed = true;
  }
}

function code(error: unknown): string {
  return error instanceof Route53Error ? error.code : 'Unknown';
}

const throwIfStopped = (ctx: JobExecutionContext): void => {
  if (ctx.signal.aborted) throw new Error('The job was stopped (the worker is draining or lost the lease).');
};

async function secretFor(ctx: JobExecutionContext, nodeId: string, action: 'publish' | 'unpublish'): Promise<VpsSecret> {
  let issued: { kind: string; material: Record<string, unknown> };
  try {
    issued = await ctx.api.jobSecret(ctx.nodeId, ctx.job.id, ctx.claimToken);
  } catch {
    // The cause is the control plane's text: not passed on.
    throw new VpsInputError('The cloud did not issue the job credential. Check that node job secrets are enabled and the Route53 key is stored.');
  }
  if (issued.kind !== VPS_SECRET_KIND) throw new VpsInputError('The job credential is not a VPS Route53 credential.');
  return parseSecret(issued.material, nodeId, action);
}

// ------------------------------------------------------------------------------------------------------------------
// Publish
// ------------------------------------------------------------------------------------------------------------------

export class VpsPublishExecutor implements JobExecutor {
  readonly type = VPS_PUBLISH_TYPE;
  readonly requiresInput = false;

  constructor(private readonly depsFactory: () => VpsDeps = () => defaultVpsDeps()) {}

  async execute(ctx: JobExecutionContext): Promise<PublishResult> {
    const { nodeId } = parseParams(ctx.params);
    const secret = await secretFor(ctx, nodeId, 'publish');
    const deps = this.depsFactory();
    const route53 = deps.route53({ accessKeyId: secret.route53AccessKeyId, secretAccessKey: secret.route53SecretAccessKey });
    const host = secret.host;
    const journal = new Journal<PublishStepName>();
    let expiresAt: Date | null = null;
    let changeId: string | null = null;
    let vpsIp: string | null = null;

    const record = (name: PublishStepName, status: StepStatus, detail: string) => {
      journal.add(name, status, detail);
      ctx.log(`vps publish step ${name}: ${status}`);
    };

    // 1. DNS UPSERT
    throwIfStopped(ctx);
    try {
      vpsIp = await vpsPublicAddress(deps.dns, secret.vpsPublicHostname);
      if (vpsIp === null) {
        record('dns_upsert', 'failed', `Could not learn the VPS public address from ${secret.vpsPublicHostname}. Check that name resolves publicly.`);
      } else if (await allResolversAnswer(deps.dns, host, vpsIp)) {
        record('dns_upsert', 'skipped', 'The host already points at the VPS on every public resolver.');
      } else {
        changeId = (await route53.upsertA(secret.hostedZoneId, host, vpsIp, TTL)).changeId;
        record('dns_upsert', 'done', 'Sent the A record (UPSERT, TTL 300) to Route53.');
      }
    } catch (error) {
      record('dns_upsert', 'failed', `Route53 refused the change (${code(error)}).`);
    }

    // 2. Wait for INSYNC, then for the public resolvers
    if (!journal.failed) {
      throwIfStopped(ctx);
      if (changeId === null) {
        record('dns_wait', 'skipped', 'Nothing to wait for: the public resolvers already answer.');
      } else {
        const outcome = await this.waitForDns(deps, route53, changeId, host, vpsIp!);
        if (outcome === 'ok') record('dns_wait', 'done', 'Route53 reports INSYNC and every public resolver answers with the VPS address.');
        else if (outcome === 'change') record('dns_wait', 'failed', 'Route53 did not report INSYNC in time. Try again in a few minutes.');
        else record('dns_wait', 'failed', 'The public resolvers do not answer with the VPS address yet. Try again in a few minutes.');
      }
    }

    // 3. Bootstrap HTTP vhost
    const patternValues = {
      nodeId,
      publicHost: host,
      tailnetIp: secret.tailnetIp!,
      tailnetName: secret.tailnetMachine!,
    };
    if (!journal.failed) {
      throwIfStopped(ctx);
      try {
        const existing = await deps.nginx.read(host);
        if (existing !== null && existing.includes(`server_name ${host};`)) {
          record('bootstrap_vhost', 'skipped', 'A vhost file for this host is already in place; it is not downgraded.');
        } else {
          await deps.nginx.apply(host, renderBootstrapVhost(patternValues));
          record('bootstrap_vhost', 'done', 'Wrote the HTTP vhost; nginx -t passed and the proxy was reloaded.');
        }
      } catch (error) {
        record('bootstrap_vhost', 'failed', nginxDetail(error, 'bootstrap vhost'));
      }
    }

    // 4. Certificate
    if (!journal.failed) {
      throwIfStopped(ctx);
      try {
        const certbot = deps.certbot();
        const result = await certbot.ensure(host);
        expiresAt = result.expiresAt;
        if (result.outcome === 'present') record('certificate', 'skipped', 'A valid certificate with working renewal settings is already in place.');
        else record('certificate', 'done', 'certbot issued the certificate; its renewal settings use the shared webroot.');
      } catch (error) {
        record('certificate', 'failed', error instanceof CertbotRefusedError || error instanceof VpsInputError ? error.message : 'The certificate step failed.');
      }
    }

    // 5. Final vhost
    if (!journal.failed) {
      throwIfStopped(ctx);
      try {
        const outcome = await deps.nginx.apply(host, renderFinalVhost(secret.servePattern!, patternValues));
        if (outcome === 'unchanged') record('final_vhost', 'skipped', 'The final vhost is already in place and identical.');
        else record('final_vhost', 'done', `Wrote the Pattern ${secret.servePattern} vhost; nginx -t passed and the proxy was reloaded.`);
      } catch (error) {
        record('final_vhost', 'failed', nginxDetail(error, 'final vhost'));
      }
    }

    // 6. Validate
    if (!journal.failed) {
      throwIfStopped(ctx);
      const verdict = await this.validate(deps, host);
      record('validate', verdict.status, verdict.detail);
    }

    return { host, steps: journal.steps, certificateExpiresAt: expiresAt === null ? null : expiresAt.toISOString() };
  }

  /** Polls GetChange, then the public resolvers. `ok`, or which wait ran out. */
  private async waitForDns(deps: VpsDeps, route53: Route53Client, changeId: string, host: string, ip: string): Promise<'ok' | 'change' | 'resolvers'> {
    const { pollMs, changeWaitMs, dnsWaitMs } = deps.timing;
    let deadline = deps.now().getTime() + changeWaitMs;
    for (;;) {
      try {
        if ((await route53.getChange(changeId)) === 'INSYNC') break;
      } catch {
        // A failed status read is retried until the deadline.
      }
      if (deps.now().getTime() >= deadline) return 'change';
      await deps.sleep(pollMs);
    }
    deadline = deps.now().getTime() + dnsWaitMs;
    for (;;) {
      if (await allResolversAnswer(deps.dns, host, ip)) return 'ok';
      if (deps.now().getTime() >= deadline) return 'resolvers';
      await deps.sleep(pollMs);
    }
  }

  /** `/health` 200 and unauthenticated `/v1/models` 401. A 200 without a key takes the vhost out of service at once. */
  private async validate(deps: VpsDeps, host: string): Promise<{ status: StepStatus; detail: string }> {
    let health = 0;
    for (let attempt = 0; attempt < deps.timing.healthTries; attempt += 1) {
      health = await deps.http.status(host, '/health');
      if (health === 200) break;
      if (attempt + 1 < deps.timing.healthTries) await deps.sleep(deps.timing.healthPauseMs);
    }
    if (health !== 200) return { status: 'failed', detail: `GET /health answered ${health === 0 ? 'nothing' : health}, expected 200. Check the node and its tailnet path.` };
    const models = await deps.http.status(host, '/v1/models');
    if (models === 200) {
      let removed = false;
      try {
        await deps.nginx.remove(host);
        removed = true;
      } catch {
        removed = false;
      }
      return {
        status: 'failed',
        detail: removed
          ? 'DANGER: GET /v1/models answered 200 without a key. The vhost was taken out of service. Fix the node (LAI-SEC-014) before publishing again.'
          : 'DANGER: GET /v1/models answered 200 without a key and the vhost could not be removed. Disable it by hand now.',
      };
    }
    if (models !== 401) return { status: 'failed', detail: `GET /v1/models without a key answered ${models === 0 ? 'nothing' : models}, expected 401.` };
    return { status: 'done', detail: 'GET /health answered 200 and GET /v1/models without a key answered 401.' };
  }
}

function nginxDetail(error: unknown, what: string): string {
  if (error instanceof NginxRefusedError) {
    return error.restored
      ? `nginx did not accept the ${what}. The previous file was restored and nginx is serving as before.`
      : `nginx did not accept the ${what} and the previous file could not be confirmed. Check nginx on the VPS by hand.`;
  }
  return `The ${what} could not be written.`;
}

// ------------------------------------------------------------------------------------------------------------------
// Unpublish
// ------------------------------------------------------------------------------------------------------------------

export class VpsUnpublishExecutor implements JobExecutor {
  readonly type = VPS_UNPUBLISH_TYPE;
  readonly requiresInput = false;

  constructor(private readonly depsFactory: () => VpsDeps = () => defaultVpsDeps()) {}

  async execute(ctx: JobExecutionContext): Promise<UnpublishResult> {
    const { nodeId } = parseParams(ctx.params);
    const secret = await secretFor(ctx, nodeId, 'unpublish');
    const deps = this.depsFactory();
    const route53 = deps.route53({ accessKeyId: secret.route53AccessKeyId, secretAccessKey: secret.route53SecretAccessKey });
    const host = secret.host;
    const journal = new Journal<UnpublishStepName>();
    const record = (name: UnpublishStepName, status: StepStatus, detail: string) => {
      journal.add(name, status, detail);
      ctx.log(`vps unpublish step ${name}: ${status}`);
    };

    // 1. Remove the vhost files, test, reload (restore on failure)
    throwIfStopped(ctx);
    try {
      const outcome = await deps.nginx.remove(host);
      if (outcome === 'absent') record('vhost_remove', 'skipped', 'No vhost file for this host; nothing to remove.');
      else record('vhost_remove', 'done', 'Took the vhost out of service (kept as a .removed file); nginx -t passed and the proxy was reloaded.');
    } catch (error) {
      record('vhost_remove', 'failed', nginxDetail(error, 'removal'));
    }

    // 2. Delete the A record
    if (!journal.failed) {
      throwIfStopped(ctx);
      try {
        const ip = await vpsPublicAddress(deps.dns, secret.vpsPublicHostname);
        if (ip === null) {
          record('dns_delete', 'failed', `Could not learn the VPS public address from ${secret.vpsPublicHostname}, which Route53 needs to match the record.`);
        } else {
          const outcome = await route53.deleteA(secret.hostedZoneId, host, ip, TTL);
          if (outcome === 'absent') record('dns_delete', 'skipped', 'The A record was already absent.');
          else record('dns_delete', 'done', 'Deleted the A record from Route53.');
        }
      } catch (error) {
        const reason = code(error);
        record('dns_delete', 'failed', reason === 'RecordValueMismatch' ? 'The A record has a different value than this agent created; delete it by hand in Route53.' : `Route53 refused the change (${reason}).`);
      }
    }

    // 3. The certificate stays
    if (!journal.failed) record('certificate_kept', 'skipped', 'The certificate is left in place to expire; it is not revoked.');

    return { host, steps: journal.steps };
  }
}

/** The executors the app CLI registers (`nodeExecutors` of `createCli`). */
export function vpsExecutors(): JobExecutor[] {
  return [new VpsPublishExecutor(), new VpsUnpublishExecutor()];
}
