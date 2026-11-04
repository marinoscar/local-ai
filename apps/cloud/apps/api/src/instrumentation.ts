// OpenTelemetry bootstrap: the FIRST import of `main.ts`. Auto-instrumentation
// can only patch modules required AFTER `sdk.start()`, so nothing that loads
// Nest may come before this file. The bootstrap itself is
// `@marinoscar/platform-api/otel-core/sdk`, a Nest-free entry.
//
//   telemetry slice off, or OTEL_ENABLED !== 'true'   installs nothing; every instrument and span is the API's no-op
//   both on                                           OTLP/HTTP traces, metrics (every 60 s) and logs to
//                                                     OTEL_EXPORTER_OTLP_ENDPOINT, each behind the runtime gate,
//                                                     which starts CLOSED: nothing is exported until an administrator
//                                                     turns collection on at /admin/settings/telemetry
//
// Reads the slice list from `@app/shared` (plain CommonJS over `slices.json`),
// which loads nothing the auto-instrumentation patches.
import { APP_SLUG, ENABLED_SLICES } from '@app/shared';
import { initializeOtel, resolveServiceName, resolveTelemetryInstanceId } from '@marinoscar/platform-api/otel-core/sdk';

export const sdk = ENABLED_SLICES.includes('telemetry')
  ? initializeOtel({
      serviceName: resolveServiceName(`${APP_SLUG}-api`),
      // The instance id the gate stamps until the telemetry settings are read: the app's slug.
      instanceId: resolveTelemetryInstanceId(null, APP_SLUG),
    })
  : null;
