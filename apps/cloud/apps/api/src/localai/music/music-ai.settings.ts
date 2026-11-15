// The `localaiMusic` system settings namespace (#570, docs/specs/cloud-app.md section 3.10): the "Use AI for music"
// switch. Default OFF: the music page uses the cloud's AI (the EnterpriseAppBase AI slice) only after an administrator
// turns this on, on top of the AI slice's own switch and a model that resolves for the person.
//
// ORG LAYER (tighten): an organization may switch music AI OFF for its members, never on when the deployment has it
// off (`system.aiEnabled && org.aiEnabled`). It is read through `SettingsResolver.resolveSystem(key, { orgId })`.
//
// WHO MAY WRITE. The deployment value only through `PUT /api/localai/music/ai-settings` (`ai_config:write`, the AI
// slice's administrator permission, so the people who configure providers and keys also decide whether music uses
// them). The platform's own `PUT`/`PATCH /api/system-settings` therefore refuse this namespace (`putSchema` and
// `wirePatchSchema` accept nothing): otherwise `system_settings:write` would be a second door. The org layer is the
// settings slice's own (`/api/org-settings`, `org_settings:write`).
//
// Registered before `SettingsModule.forRoot()` (`platform/platform.ts` imports this file first).
import { registerSystemSettingsNamespaces, type SystemSettingsNamespace } from '@marinoscar/platform-api/settings';
import { z } from 'zod';

export const MUSIC_AI_SETTINGS_NAMESPACE = 'localaiMusic';

const storedSchema = z.object({ aiEnabled: z.boolean() }).strict();
const patchSchema = storedSchema.partial();

/** The stored value. */
export type LocalaiMusicSettings = z.infer<typeof storedSchema>;

export const LOCALAI_MUSIC_DEFAULTS: LocalaiMusicSettings = { aiEnabled: false };

/** A damaged stored value reads as the default (off), never as a failure. */
export function readLocalaiMusicSettings(stored: unknown): LocalaiMusicSettings {
  const parsed = storedSchema.safeParse(stored);
  if (parsed.success) return parsed.data;
  const aiEnabled = typeof stored === 'object' && stored !== null ? (stored as { aiEnabled?: unknown }).aiEnabled : undefined;
  return { aiEnabled: typeof aiEnabled === 'boolean' ? aiEnabled : LOCALAI_MUSIC_DEFAULTS.aiEnabled };
}

export const LOCALAI_MUSIC_SYSTEM_SETTINGS = {
  key: MUSIC_AI_SETTINGS_NAMESPACE,
  description: 'Local AI music: whether the music page may use the cloud AI to write lyrics and titles (written only by PUT /api/localai/music/ai-settings).',
  storedSchema,
  patchSchema,
  // Refused on the platform's own routes on purpose (see the header): the music route checks ai_config:write.
  putSchema: z.never().optional(),
  wirePatchSchema: z.never().optional(),
  responseSchema: storedSchema,
  defaults: LOCALAI_MUSIC_DEFAULTS,
  requiredOnPut: false,
  read: (stored: unknown) => readLocalaiMusicSettings(stored),
  merge: (current: LocalaiMusicSettings, patch: Partial<LocalaiMusicSettings> | undefined) => ({ ...current, ...(patch ?? {}) }),
  org: {
    schema: patchSchema,
    // Tighten only: the deployment's switch is a ceiling.
    merge: (system: LocalaiMusicSettings, org: Partial<LocalaiMusicSettings>) => ({ aiEnabled: system.aiEnabled && (org.aiEnabled ?? true) }),
    mode: 'tighten',
    readPermission: 'org_settings:read',
    writePermission: 'org_settings:write',
  },
} satisfies SystemSettingsNamespace<'localaiMusic', LocalaiMusicSettings, Partial<LocalaiMusicSettings>>;

// Types `SystemSettingsService.getNamespace('localaiMusic')`.
declare module '@marinoscar/platform-api/settings' {
  interface SystemSettingsNamespaces {
    localaiMusic: LocalaiMusicSettings;
  }
}

registerSystemSettingsNamespaces([LOCALAI_MUSIC_SYSTEM_SETTINGS as unknown as SystemSettingsNamespace]);
