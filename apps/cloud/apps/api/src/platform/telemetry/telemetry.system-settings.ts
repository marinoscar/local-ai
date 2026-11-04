// The `telemetry` system settings namespace: whether telemetry is exported, how
// long it is kept, the bounds of an ad-hoc query and the assistant over it. The
// slice owns the shape (the contract's `telemetrySettingsSchema`), the defaults
// and the merge rule; this file binds them to the app's settings registry. The
// slice registers it before `SettingsModule.forRoot()` (`contribute()`), and it
// carries no credential: the GreptimeDB passwords are in the credential store.
import {
  TELEMETRY_SETTINGS_DEFAULTS,
  TELEMETRY_SETTINGS_DESCRIPTION,
  TELEMETRY_SETTINGS_NAMESPACE,
  mergeTelemetrySettings,
  telemetrySettingsSchema,
  type TelemetrySettings,
} from '@marinoscar/platform-api/telemetry';
import type { SystemSettingsNamespace } from '@marinoscar/platform-api/settings';
import { telemetryInstanceIdSchema } from '@marinoscar/platform-contract/telemetry';
import { z } from 'zod';

/** A partial update, one level deep (zod 4 has no deep partial). `null` clears `instanceId`, `provider` and `modelId`. */
export const telemetrySettingsPatchSchema = z.object({
  enabled: z.boolean().optional(),
  retentionDays: z.number().int().min(1).max(3650).optional(),
  instanceId: telemetryInstanceIdSchema.nullable().optional(),
  query: z
    .object({
      maxRows: z.number().int().min(1).max(100000).optional(),
      timeoutSeconds: z.number().int().min(1).max(120).optional(),
    })
    .optional(),
  assistant: z
    .object({
      enabled: z.boolean().optional(),
      provider: z.string().nullable().optional(),
      modelId: z.string().nullable().optional(),
      shareResults: z.boolean().optional(),
      maxResultRowsToModel: z.number().int().min(1).max(100).optional(),
      maxSteps: z.number().int().min(1).max(20).optional(),
    })
    .optional(),
});

export const TELEMETRY_SYSTEM_SETTINGS = {
  key: TELEMETRY_SETTINGS_NAMESPACE,
  description: TELEMETRY_SETTINGS_DESCRIPTION,
  storedSchema: telemetrySettingsSchema,
  patchSchema: telemetrySettingsPatchSchema,
  putSchema: telemetrySettingsSchema,
  wirePatchSchema: telemetrySettingsPatchSchema,
  responseSchema: null,
  // A copy, so nothing the app does to its defaults reaches the package's.
  defaults: structuredClone(TELEMETRY_SETTINGS_DEFAULTS),
  requiredOnPut: false,
  merge: mergeTelemetrySettings,
} satisfies SystemSettingsNamespace<'telemetry', TelemetrySettings, z.infer<typeof telemetrySettingsPatchSchema>>;

// Types `SystemSettingsService.getNamespace('telemetry')` as `TelemetrySettings`.
declare module '@marinoscar/platform-api/settings' {
  interface SystemSettingsNamespaces {
    telemetry: TelemetrySettings;
  }
}
