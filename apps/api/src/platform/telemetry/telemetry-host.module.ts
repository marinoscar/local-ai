// The app's binding of the telemetry slice's host ports, passed to
// `TelemetryModule.forRoot({ imports: [...] })`:
//
//   TELEMETRY_AUDIT_SINK       audit_events (JSON `meta`)
//   TELEMETRY_SETTINGS_STORE   the `telemetry` namespace, its provenance, one keyed row, feature flags
//   TELEMETRY_CREDENTIAL_STORE the credential service itself (the GreptimeDB passwords)
//   TELEMETRY_JOBS             the platform queue
//   TELEMETRY_AI               AiService.forUser while the `ai` slice is mounted, else a 403 AI_DISABLED stub
//   TELEMETRY_APP_INFO         slug, service name, version, deploy document
//
// Built by a function because the AI port depends on which slices are mounted.
import { Module, type Provider, type Type } from '@nestjs/common';
import { CredentialsModule, CredentialsService } from '@marinoscar/platform-api/credentials';
import {
  TELEMETRY_AI,
  TELEMETRY_APP_INFO,
  TELEMETRY_AUDIT_SINK,
  TELEMETRY_CREDENTIAL_STORE,
  TELEMETRY_JOBS,
  TELEMETRY_SETTINGS_STORE,
  type TelemetryCredentialStore,
} from '@marinoscar/platform-api/telemetry';

import {
  TelemetryAppInfoAdapter,
  TelemetryAuditSinkAdapter,
  TelemetryJobsAdapter,
  TelemetryNoAiAdapter,
  TelemetrySettingsStoreAdapter,
} from './telemetry-adapters';

/** Compile-time proof that the credential service IS the credential port, unadapted. */
type CredentialsServiceIsTelemetryCredentialStore = CredentialsService extends TelemetryCredentialStore ? true : never;
const _credentialsFit: CredentialsServiceIsTelemetryCredentialStore = true;
void _credentialsFit;

/** The host module for the telemetry slice. `ai`: whether the AI slice is mounted. */
export function createTelemetryHostModule(options: { ai: boolean }): Type<unknown> {
  // Loaded only when the AI slice is mounted: the adapter imports the AI package.
  const aiBinding: { imports: Type<unknown>[]; provider: Provider } = options.ai
    ? {
        imports: [(require('../ai/ai.config') as typeof import('../ai/ai.config')).AiModule as unknown as Type<unknown>],
        provider: {
          provide: TELEMETRY_AI,
          useClass: (require('./telemetry-ai.adapter') as typeof import('./telemetry-ai.adapter')).TelemetryAiAdapter,
        },
      }
    : { imports: [], provider: { provide: TELEMETRY_AI, useClass: TelemetryNoAiAdapter } };

  const ports: Array<Provider & { provide: unknown }> = [
    { provide: TELEMETRY_AUDIT_SINK, useClass: TelemetryAuditSinkAdapter },
    { provide: TELEMETRY_SETTINGS_STORE, useClass: TelemetrySettingsStoreAdapter },
    { provide: TELEMETRY_CREDENTIAL_STORE, useExisting: CredentialsService },
    { provide: TELEMETRY_JOBS, useClass: TelemetryJobsAdapter },
    aiBinding.provider as Provider & { provide: unknown },
    { provide: TELEMETRY_APP_INFO, useClass: TelemetryAppInfoAdapter },
  ];

  @Module({
    imports: [CredentialsModule, ...aiBinding.imports],
    providers: ports,
    exports: ports.map((port) => port.provide as never),
  })
  class TelemetryHostModule {}

  return TelemetryHostModule;
}
