// The telemetry slice: the OpenTelemetry export gate an administrator opens at
// runtime, the GreptimeDB connection, the SQL explorer, the Telemetry Dashboard
// with its metric groups and health verdict, the stack-agent surface for a VPS,
// two server-only jobs and the `telemetry.*` Doctor checks. The emitting half
// (the OpenTelemetry SDK, the metric name registry, `@Trace()`) is
// `@marinoscar/platform-api/otel-core`, part of the always-mounted host core, so
// this slice only decides whether anything is exported and where to.
//
// Off until an administrator saves a GreptimeDB connection and switches
// collection on at `/admin/settings/telemetry`: no environment variable.
// The starter's SDK bootstrap is `src/instrumentation.ts`; it starts only while
// this slice is enabled AND `OTEL_ENABLED=true`.
//
// The minimal example is `activity.metric-group.ts`: a dashboard section.
import type { ApiSlice } from '../slices/slice';

export const telemetrySlice: ApiSlice = {
  id: 'telemetry',
  permissionSlices: ['telemetry'],
  contribute: () => {
    const { TELEMETRY_SYSTEM_SETTINGS } = require('./telemetry.system-settings') as typeof import('./telemetry.system-settings');
    const { TELEMETRY_GREPTIME_CREDENTIAL_PURPOSE } = require('@marinoscar/platform-api/telemetry') as typeof import('@marinoscar/platform-api/telemetry');
    return {
      systemSettings: [TELEMETRY_SYSTEM_SETTINGS],
      // The two GreptimeDB passwords an administrator saves with the connection (deployment tier only).
      credentialPurposes: [
        {
          purpose: TELEMETRY_GREPTIME_CREDENTIAL_PURPOSE,
          owner: 'telemetry',
          label: 'GreptimeDB reader and admin passwords',
          tiers: ['system'],
        },
      ],
    };
  },
  modules: (enabled) => {
    const { buildTelemetryModule } = require('./telemetry.config') as typeof import('./telemetry.config');
    return [buildTelemetryModule({ ai: enabled.has('ai') })];
  },
};
