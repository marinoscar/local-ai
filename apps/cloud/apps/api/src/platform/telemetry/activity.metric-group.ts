// The slice's minimal example: one Telemetry Dashboard group of the app's own.
// ONE file like this and ONE option (`metricGroups` in `telemetry.config.ts`):
// no platform file, no web file and no collector change, because the dashboard
// renders whatever groups the API reports. It charts two counters this API
// already emits (the host core's `app.auth.*` instruments, which the identity
// slice records), named by the table rule (dots become `_`, `_total` is
// appended). A family is skipped, never an error, while its table does not
// exist yet: a fresh stack shows the section as "no data yet".
//
// A group for your own feature follows the same shape: register the metric name
// (`registerAppMetrics` of `@marinoscar/platform-api/otel-core`), record it with
// `AppMetricsService.add(key, 1, labels)` and list a group for its table here.
// The id is permanent once dashboards use it.
import type { MetricGroupDef } from '@marinoscar/platform-api/telemetry';

declare module '@marinoscar/platform-api/telemetry' {
  interface MetricGroupIds {
    /** Sign-ins and token refreshes by outcome. */
    activity: true;
  }
}

/** The API's `app_*` tables carry the service and the instance. */
const FILTERS = ['service', 'instance'] as const;

/** "App activity": sign-ins and token refreshes by outcome, over the window. Served by `GET /api/admin/telemetry/dashboard/metrics?group=activity`. */
export const ACTIVITY_METRIC_GROUP: MetricGroupDef = {
  id: 'activity',
  label: 'App activity',
  title: 'App activity',
  // The platform's six groups use 10 to 60; an app's go after them.
  order: 70,
  description: 'sign-ins and token refreshes by outcome',
  families: [
    {
      key: 'authLogins',
      group: 'activity',
      label: 'Sign-ins',
      table: 'app_auth_logins_total',
      kind: 'counter',
      unit: 'count',
      rate: 'count',
      groupBy: 'outcome',
      requiredColumns: ['outcome'],
      filters: FILTERS,
    },
    {
      key: 'authRefreshes',
      group: 'activity',
      label: 'Token refreshes',
      table: 'app_auth_refreshes_total',
      kind: 'counter',
      unit: 'count',
      rate: 'count',
      groupBy: 'outcome',
      requiredColumns: ['outcome'],
      filters: FILTERS,
    },
  ],
};

/** Your own groups: append here. */
export const APP_METRIC_GROUPS: readonly MetricGroupDef[] = [ACTIVITY_METRIC_GROUP];
