// The Route53 key of the VPS stack-agent in the deployment credential store (docs/specs/cloud-app.md section 4.10).
// Three entries of the `localai_route53` purpose make one key: `access_key_id`, `secret_access_key`, `hosted_zone_id`.
//
// SECRET HANDLING. `set` is the only writer and `read` the only reader of plaintext, and `read` is for the per-job
// secret broker only. `status` returns what the admin page may show: whether a key is stored, the credential store's
// masked hints, when it was saved. Nothing here logs, audits or throws a key; the audit row carries the hint only.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { CredentialsService } from '@marinoscar/platform-api/credentials';
import { z } from 'zod';

import { LOCALAI_ROUTE53_PURPOSE } from '../../platform/credentials/credential-purposes';
import { AUDIT_VPS_ROUTE53_KEY_CLEARED, AUDIT_VPS_ROUTE53_KEY_SET } from './vps.constants';

const PURPOSE = LOCALAI_ROUTE53_PURPOSE.purpose;
const ENTRY = { id: 'access_key_id', secret: 'secret_access_key', zone: 'hosted_zone_id' } as const;

/** The body of `PUT /api/localai/vps/route53`. Messages are fixed text: they never echo the value that failed. */
export const Route53KeyBodySchema = z
  .object({
    accessKeyId: z.string().trim().regex(/^(?:AKIA|ASIA)[A-Z0-9]{12,32}$/, 'accessKeyId must be an AWS access key id'),
    secretAccessKey: z.string().trim().regex(/^[A-Za-z0-9/+=]{30,80}$/, 'secretAccessKey must be an AWS secret access key'),
    hostedZoneId: z.string().trim().regex(/^Z[A-Z0-9]{1,31}$/, 'hostedZoneId must be a Route53 hosted zone id'),
  })
  .strict();
export type Route53KeyBody = z.infer<typeof Route53KeyBodySchema>;

/** What the admin page may show. */
export interface Route53KeyStatus {
  configured: boolean;
  /** The credential store's masked hint of the access key id (never the key). */
  accessKeyHint: string | null;
  hostedZoneHint: string | null;
  updatedAt: string | null;
}

/** The plaintext key, for the broker only. */
export interface Route53Key {
  accessKeyId: string;
  secretAccessKey: string;
  hostedZoneId: string;
}

/** A stored key exists but cannot be decrypted (`SECRETS_ENCRYPTION_KEY` lost or changed): LAI-CLD-032. */
export class Route53KeyUnreadableError extends Error {
  constructor() {
    super('The stored Route53 key cannot be read');
    this.name = 'Route53KeyUnreadableError';
  }
}

@Injectable()
export class Route53KeyService {
  private readonly logger = new Logger(Route53KeyService.name);

  constructor(
    private readonly credentials: CredentialsService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  async status(): Promise<Route53KeyStatus> {
    const [id, secret, zone] = await Promise.all([
      this.credentials.describe(PURPOSE, ENTRY.id),
      this.credentials.describe(PURPOSE, ENTRY.secret),
      this.credentials.describe(PURPOSE, ENTRY.zone),
    ]);
    const configured = id !== null && secret !== null && zone !== null;
    const updated = [id, secret, zone].filter((entry) => entry !== null).map((entry) => entry!.updatedAt.getTime());
    return {
      configured,
      accessKeyHint: configured ? id!.hint : null,
      hostedZoneHint: configured ? zone!.hint : null,
      updatedAt: configured ? new Date(Math.max(...updated)).toISOString() : null,
    };
  }

  /** Stores (replaces) the key. The secret half is written first, so a half-written key never reads as configured. */
  async set(body: Route53KeyBody, actorUserId: string | null): Promise<Route53KeyStatus> {
    const meta = { label: 'VPS stack-agent Route53 key', updatedByUserId: actorUserId };
    await this.credentials.setSecret(PURPOSE, ENTRY.secret, body.secretAccessKey, meta);
    await this.credentials.setSecret(PURPOSE, ENTRY.id, body.accessKeyId, meta);
    await this.credentials.setSecret(PURPOSE, ENTRY.zone, body.hostedZoneId, meta);
    const status = await this.status();
    await this.record(AUDIT_VPS_ROUTE53_KEY_SET, actorUserId, { accessKeyHint: status.accessKeyHint });
    return status;
  }

  /** Removes the key. Idempotent. */
  async clear(actorUserId: string | null): Promise<Route53KeyStatus> {
    await this.credentials.deleteSecret(PURPOSE, ENTRY.id);
    await this.credentials.deleteSecret(PURPOSE, ENTRY.secret);
    await this.credentials.deleteSecret(PURPOSE, ENTRY.zone);
    await this.record(AUDIT_VPS_ROUTE53_KEY_CLEARED, actorUserId, {});
    return this.status();
  }

  /**
   * SERVER-SIDE ONLY. RETURNS PLAINTEXT. For the per-job secret broker; never call it from a controller or put its
   * result in a response, a log line, an error or an audit row. Null when no complete key is stored.
   *
   * @throws Route53KeyUnreadableError when a key is stored but cannot be decrypted.
   */
  async read(): Promise<Route53Key | null> {
    try {
      const [accessKeyId, secretAccessKey, hostedZoneId] = await Promise.all([
        this.credentials.getSecret(PURPOSE, ENTRY.id),
        this.credentials.getSecret(PURPOSE, ENTRY.secret),
        this.credentials.getSecret(PURPOSE, ENTRY.zone),
      ]);
      if (accessKeyId === null || secretAccessKey === null || hostedZoneId === null) return null;
      return { accessKeyId, secretAccessKey, hostedZoneId };
    } catch (error) {
      this.logger.error(`reading the Route53 key failed: ${error instanceof Error ? error.name : 'unknown error'}`);
      throw new Route53KeyUnreadableError();
    }
  }

  /** After the write committed. A failed audit write is logged by error name and never undoes the change. */
  private async record(action: string, actorUserId: string | null, meta: Record<string, string | number | boolean | null>): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId, targetType: 'localai_vps', targetId: 'route53', meta });
    } catch (error) {
      this.logger.error(`audit ${action} failed: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }
}
