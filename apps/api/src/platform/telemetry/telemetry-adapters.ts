// The app's adapters for the telemetry slice's host ports. Each is the smallest
// binding that satisfies the port; the slice injects capabilities by token and
// never imports app code. `TELEMETRY_CREDENTIAL_STORE` is bound to the
// credential service itself (see `telemetry-host.module.ts`).
import { ForbiddenException, Injectable, type Logger } from '@nestjs/common';
import type { Prisma } from '@prisma/client';
import { APP_SLUG } from '@app/shared';
import type { TelemetrySettings } from '@marinoscar/platform-contract/telemetry';
import { readDeployInfo, resolveApiVersion, resolveDeployInfoPath } from '@marinoscar/platform-api/host';
import { JobHandlerRegistry, JobsService, enqueueHousekeepingJob, type JobHandler } from '@marinoscar/platform-api/jobs';
import { resolveServiceName } from '@marinoscar/platform-api/otel-core/sdk';
import { SystemSettingsService } from '@marinoscar/platform-api/settings';
import type {
  TelemetryAiError,
  TelemetryAiPort,
  TelemetryAiSession,
  TelemetryAiTool,
  TelemetryAppInfo,
  TelemetryAuditEvent,
  TelemetryAuditSink,
  TelemetryDeployInfo,
  TelemetryFeatureFlag,
  TelemetryJobHandler,
  TelemetryJobRecord,
  TelemetryJobsPort,
  TelemetrySettingsProvenance,
  TelemetrySettingsRow,
  TelemetrySettingsStore,
} from '@marinoscar/platform-api/telemetry';

import { PrismaService } from '../../prisma/prisma.service';
import { TELEMETRY_SYSTEM_SETTINGS } from './telemetry.system-settings';

/** `TELEMETRY_AUDIT_SINK`: telemetry's audit events into `audit_events` (the `meta` column is JSON). */
@Injectable()
export class TelemetryAuditSinkAdapter implements TelemetryAuditSink {
  constructor(private readonly prisma: PrismaService) {}

  async record(event: TelemetryAuditEvent): Promise<void> {
    await this.prisma.auditEvent.create({
      data: {
        actorUserId: event.actorUserId,
        action: event.action,
        targetType: event.targetType,
        targetId: event.targetId,
        ...(event.meta === undefined ? {} : { meta: event.meta as Prisma.InputJsonValue }),
      },
    });
  }
}

/**
 * The keyed `system_settings` rows telemetry may read and write: the GreptimeDB
 * connection and the settings of a non-built-in store (`telemetry_store`).
 */
export const TELEMETRY_OWNED_ROW_KEYS: readonly string[] = ['telemetry_connection', 'telemetry_store'];

const PROVENANCE_SELECT = {
  version: true,
  updatedAt: true,
  updatedByUser: { select: { id: true, email: true } },
} as const;

function assertOwnedKey(key: string): void {
  if (!TELEMETRY_OWNED_ROW_KEYS.includes(key)) {
    throw new Error(`Telemetry may not access the system settings row "${key}".`);
  }
}

/**
 * `TELEMETRY_SETTINGS_STORE`: the `telemetry` namespace through the settings
 * slice (its cached validation, its If-Match re-check, its audit row), the
 * provenance read, telemetry's one keyed row (least privilege: only the keys
 * above) and an allowlist of feature flags.
 *
 * A flag whose namespace belongs to a slice this app does not mount reads as
 * `false` (the settings document has no such block).
 */
@Injectable()
export class TelemetrySettingsStoreAdapter implements TelemetrySettingsStore {
  constructor(
    private readonly prisma: PrismaService,
    private readonly systemSettings: SystemSettingsService,
  ) {}

  getTelemetryPolicy(): Promise<TelemetrySettings> {
    return this.systemSettings.getNamespace(TELEMETRY_SYSTEM_SETTINGS);
  }

  async replaceTelemetryPolicy(next: TelemetrySettings, actorUserId: string, expectedVersion?: number): Promise<void> {
    await this.systemSettings.patchSettings(
      { telemetry: next } as unknown as Parameters<SystemSettingsService['patchSettings']>[0],
      actorUserId,
      expectedVersion,
    );
  }

  async readPolicyProvenance(): Promise<TelemetrySettingsProvenance | null> {
    const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' }, select: PROVENANCE_SELECT });
    return row ? { version: row.version, updatedAt: row.updatedAt, updatedBy: row.updatedByUser } : null;
  }

  async readRow(key: string): Promise<TelemetrySettingsRow | null> {
    assertOwnedKey(key);
    const row = await this.prisma.systemSettings.findUnique({ where: { key }, select: { value: true, ...PROVENANCE_SELECT } });
    return row ? { value: row.value, version: row.version, updatedAt: row.updatedAt, updatedBy: row.updatedByUser } : null;
  }

  async writeRow(key: string, value: unknown, actorUserId: string): Promise<void> {
    assertOwnedKey(key);
    await this.prisma.systemSettings.upsert({
      where: { key },
      update: { value: value as Prisma.InputJsonValue, updatedByUserId: actorUserId, version: { increment: 1 } },
      create: { key, value: value as Prisma.InputJsonValue, updatedByUserId: actorUserId },
    });
  }

  async deleteRow(key: string): Promise<void> {
    assertOwnedKey(key);
    await this.prisma.systemSettings.deleteMany({ where: { key } });
  }

  async readFeatureFlag(flag: TelemetryFeatureFlag): Promise<boolean> {
    switch (flag) {
      case 'ai':
        return this.flag('ai', 'enabled');
      case 'maintenanceMode':
        return this.flag('maintenance', 'enabled');
      case 'databaseBackup':
        return this.flag('databaseBackup', 'enabled');
      case 'browserNotifications':
        return this.flag('notifications', 'browserEnabled');
      case 'nodeJobSecretBroker':
        return this.flag('nodes', 'jobSecretBrokerEnabled');
      default: {
        const unknown: never = flag;
        throw new Error(`Unknown telemetry feature flag "${String(unknown)}".`);
      }
    }
  }

  private async flag(namespace: string, field: string): Promise<boolean> {
    const value = (await this.systemSettings.readNamespaceValue(namespace)) as Record<string, unknown> | undefined;
    return value?.[field] === true;
  }
}

/** `TELEMETRY_JOBS`: the platform queue (enqueue, housekeeping enqueue, the handler registry, two job reads and writes). */
@Injectable()
export class TelemetryJobsAdapter implements TelemetryJobsPort {
  constructor(
    private readonly jobs: JobsService,
    private readonly registry: JobHandlerRegistry,
    private readonly prisma: PrismaService,
  ) {}

  enqueue(input: { type: string; reason: 'upload' | 'rerun' | 'backfill'; payload?: Record<string, unknown> }): Promise<{ id: string; status: string }> {
    return this.jobs.enqueue({
      type: input.type,
      reason: input.reason,
      ...(input.payload === undefined ? {} : { payload: input.payload as Prisma.InputJsonValue }),
    });
  }

  async enqueueHousekeepingJob(options: { type: string; what: string; logger: Logger }): Promise<void> {
    await enqueueHousekeepingJob({ jobs: this.jobs, prisma: this.prisma, ...options });
  }

  registerHandler(handler: TelemetryJobHandler): void {
    // Neither telemetry job type is node-eligible: both hold a credential a node never gets.
    const appHandler: JobHandler = handler;
    this.registry.register(appHandler);
  }

  findLatest(type: string): Promise<TelemetryJobRecord | null> {
    return this.prisma.job.findFirst({ where: { type }, orderBy: { createdAt: 'desc' } });
  }

  async updatePayload(jobId: string, payload: Record<string, unknown>): Promise<void> {
    await this.prisma.job.update({ where: { id: jobId }, data: { payload: payload as Prisma.InputJsonValue } });
  }
}

/** `TELEMETRY_APP_INFO`: who this application is (slug, service name, API version, the deploy document). */
@Injectable()
export class TelemetryAppInfoAdapter implements TelemetryAppInfo {
  readonly slug = APP_SLUG;

  serviceName(): string {
    return resolveServiceName(`${APP_SLUG}-api`);
  }

  apiVersion(): string {
    return resolveApiVersion(__dirname);
  }

  readDeployInfo(): Promise<TelemetryDeployInfo> {
    return readDeployInfo(resolveDeployInfoPath());
  }
}

/**
 * `TELEMETRY_AI` while the AI slice is NOT mounted: the explorer's assistant
 * answers 403 `AI_DISABLED` (the same refusal the AI platform gives while it is
 * switched off) and nothing else changes. Enable the `ai` slice and the
 * assistant is bound to `AiService.forUser` instead (`telemetry-ai.adapter.ts`).
 */
@Injectable()
export class TelemetryNoAiAdapter implements TelemetryAiPort {
  forUser(): TelemetryAiSession {
    throw this.disabled();
  }

  defineTool(): TelemetryAiTool {
    throw this.disabled();
  }

  isAiError(_error: unknown): _error is TelemetryAiError {
    return false;
  }

  async assertEnabled(): Promise<void> {
    throw this.disabled();
  }

  private disabled(): ForbiddenException {
    return new ForbiddenException({ message: 'AI features are not available in this deployment.', code: 'AI_DISABLED' });
  }
}
