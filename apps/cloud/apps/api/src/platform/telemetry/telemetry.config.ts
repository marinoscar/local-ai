// The app's binding of the telemetry slice: `/api/admin/telemetry/*` and
// `/api/telemetry/config`, guarded by the app's own `@Auth()` (`platformHost`),
// every app capability through the host ports `createTelemetryHostModule` binds,
// and the app's own dashboard group after the six platform groups. The
// verdict thresholds keep their defaults; tune them with
// `dashboard.verdictThresholds`.
import { TelemetryModule } from '@marinoscar/platform-api/telemetry';

import { platformHost } from '../host';
import { APP_METRIC_GROUPS } from './activity.metric-group';
import { createTelemetryHostModule } from './telemetry-host.module';

/** `ai`: whether the AI slice is mounted (it decides what backs the explorer's assistant). */
export function buildTelemetryModule(options: { ai: boolean }) {
  return TelemetryModule.forRoot({
    host: platformHost,
    imports: [createTelemetryHostModule(options)],
    metricGroups: APP_METRIC_GROUPS,
  });
}
