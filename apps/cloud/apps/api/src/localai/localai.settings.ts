// The `localai` system settings namespace (docs/specs/cloud-app.md section 3.10 and docs/specs/gateway.md section 3.7).
//
// ONE table, `LOCALAI_SETTING_DEFS`, is the single source for everything about a key: its dotted name (the name the
// spec, `PUT /api/localai/settings` and the admin UI use), the place it has in the stored document, its Zod schema and
// its default. The stored schema, the defaults, the merge and the salvage read below are all derived from it, so they
// cannot disagree.
//
// STORED SHAPE. Nested, `system_settings.global.value.localai.<group>.<key>`: `{ gateway: { defaults: { user: {...} } } }`.
// The readers that pre-date this namespace (`alert-settings.ts`, `catalog-settings.ts`, `wizard-settings.ts`) accept the
// nested form and the dotted form; this namespace writes the nested one and reads both.
//
// WHO MAY WRITE. Only `PUT /api/localai/settings` (`settings.controller.ts`), which checks the permission per key
// prefix (`gateway.*` needs `gateway:admin`, everything else `lab:admin`). The platform's own
// `PUT`/`PATCH /api/system-settings` therefore refuse this namespace outright (`putSchema` and `wirePatchSchema` accept
// nothing): otherwise `system_settings:write` would be a second door past the per-prefix rule. The route calls
// `SystemSettingsService.patchSettings({ localai: patch })` directly, which does not go through those wire schemas.
//
// Registered before `SettingsModule.forRoot()` (`platform/platform.ts` imports this file first).
import { registerSystemSettingsNamespaces, type SettingsReadHelpers, type SystemSettingsNamespace } from '@marinoscar/platform-api/settings';
import { z } from 'zod';

import { LOCALAI_PERMISSIONS } from './localai.permissions';

export const LOCALAI_SETTINGS_NAMESPACE = 'localai';

/** The settings keys whose prefix puts them under `gateway:admin`. Everything else is `lab:admin`. */
export const GATEWAY_SETTING_PREFIX = 'gateway.';

const int = (min: number, max: number) => z.number().int().min(min).max(max);

/** A browser origin the gateway may answer a preflight for: scheme, host and optional port, no path, never `*`. */
const ORIGIN = /^https?:\/\/[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:\d{1,5})?$/;

const userDefaults = z
  .object({
    rpm: int(1, 100_000),
    concurrent: int(1, 1_000),
    dailyTokens: int(1, Number.MAX_SAFE_INTEGER),
    expiresInDays: int(1, 3_650),
  })
  .strict();

const serviceDefaults = z
  .object({
    rpm: int(1, 100_000),
    concurrent: int(1, 1_000),
    dailyTokens: int(1, Number.MAX_SAFE_INTEGER).nullable(),
    expiresInDays: int(1, 3_650).nullable(),
  })
  .strict();

const timeouts = z
  .object({
    connectMs: int(100, 60_000),
    firstByteMs: int(1_000, 3_600_000),
    idleMs: int(1_000, 3_600_000),
    totalMs: int(1_000, 7_200_000),
    nonStreamTotalMs: int(1_000, 7_200_000),
  })
  .strict();

/** One setting: the dotted key, its schema and its default. The dotted key IS the path in the stored document. */
export interface SettingDef {
  readonly key: string;
  readonly schema: z.ZodType;
  readonly default: unknown;
}

export const LOCALAI_SETTING_DEFS: readonly SettingDef[] = [
  { key: 'alerts.driveStaleDays', schema: int(1, 3_650), default: 30 },
  { key: 'alerts.driveCriticalDays', schema: int(1, 3_650), default: 90 },
  { key: 'alerts.nodeStaleMinutes', schema: int(1, 10_080), default: 10 },
  { key: 'alerts.nodeOfflineHours', schema: int(1, 8_760), default: 24 },
  { key: 'alerts.catalogBehindDays', schema: int(1, 3_650), default: 14 },
  { key: 'alerts.repeatHours', schema: int(1, 8_760), default: 24 },
  { key: 'catalog.githubRepo', schema: z.string().regex(/^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/).refine((v) => !v.includes('..'), 'must not contain ..'), default: 'marinoscar/local-ai' },
  { key: 'agent.latestVersion', schema: z.string().regex(/^\d{1,5}\.\d{1,5}\.\d{1,5}$/).nullable(), default: null },
  { key: 'vps.stagingSuffix', schema: z.string().regex(/^[a-z0-9-]{0,32}$/), default: '' },
  { key: 'checklist.vpsPublicHostname', schema: z.string().regex(/^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$/), default: 'ai.marin.cr' },
  { key: 'gateway.defaults.user', schema: userDefaults, default: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000, expiresInDays: 90 } },
  { key: 'gateway.defaults.service', schema: serviceDefaults, default: { rpm: 600, concurrent: 16, dailyTokens: null, expiresInDays: null } },
  { key: 'gateway.maxUserKeyDays', schema: int(1, 3_650), default: 365 },
  { key: 'gateway.timeouts', schema: timeouts, default: { connectMs: 5_000, firstByteMs: 180_000, idleMs: 120_000, totalMs: 1_800_000, nonStreamTotalMs: 900_000 } },
  { key: 'gateway.maxAttempts', schema: int(1, 10), default: 3 },
  { key: 'gateway.queueFactor', schema: z.number().positive().max(64), default: 4 },
  { key: 'gateway.corsOrigins', schema: z.array(z.string().regex(ORIGIN, 'must be an origin such as https://chat.example.test (no path, never *)')).max(20), default: [] },
  { key: 'gateway.usageRetentionDays', schema: int(1, 3_650), default: 90 },
  { key: 'gateway.tailnetSuffix', schema: z.string().regex(/^[a-z0-9-]{1,63}\.ts\.net$/).nullable(), default: null },
];

const BY_KEY: ReadonlyMap<string, SettingDef> = new Map(LOCALAI_SETTING_DEFS.map((def) => [def.key, def]));

/** The definition of a dotted key, or undefined for an unknown key. */
export function settingDef(key: string): SettingDef | undefined {
  return BY_KEY.get(key);
}

/** The permission `PUT /api/localai/settings` needs to change `key`: `gateway:admin` for `gateway.*`, `lab:admin` otherwise. */
export function settingPermission(key: string): string {
  return key.startsWith(GATEWAY_SETTING_PREFIX) ? LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id : LOCALAI_PERMISSIONS.LAB_ADMIN.id;
}

// ---------------------------------------------------------------------------------------------------------------
// Nested <-> dotted
// ---------------------------------------------------------------------------------------------------------------

type Tree = { [segment: string]: unknown };

const isTree = (value: unknown): value is Tree => typeof value === 'object' && value !== null && !Array.isArray(value);

/** Writes `value` at the dotted `key` of `target`, creating the groups on the way. */
export function setAtKey(target: Tree, key: string, value: unknown): void {
  const path = key.split('.');
  let node = target;
  for (const segment of path.slice(0, -1)) {
    const next = node[segment];
    if (isTree(next)) node = next;
    else {
      const created: Tree = {};
      node[segment] = created;
      node = created;
    }
  }
  node[path[path.length - 1]!] = value;
}

/** Reads the dotted `key` from a nested tree, or undefined. */
export function getAtKey(source: unknown, key: string): unknown {
  let node: unknown = source;
  for (const segment of key.split('.')) {
    if (!isTree(node)) return undefined;
    node = node[segment];
  }
  return node;
}

/** A nested tree from a flat map of dotted keys. */
export function nest(flat: Readonly<Record<string, unknown>>): Tree {
  const tree: Tree = {};
  for (const [key, value] of Object.entries(flat)) setAtKey(tree, key, structuredClone(value));
  return tree;
}

/** The flat map of dotted keys of a stored (nested) value. */
export function flatten(stored: unknown): Record<string, unknown> {
  const flat: Record<string, unknown> = {};
  for (const def of LOCALAI_SETTING_DEFS) flat[def.key] = getAtKey(stored, def.key);
  return flat;
}

// ---------------------------------------------------------------------------------------------------------------
// The namespace
// ---------------------------------------------------------------------------------------------------------------

/** The stored value: every key, complete. */
export type LocalaiSettings = {
  alerts: { driveStaleDays: number; driveCriticalDays: number; nodeStaleMinutes: number; nodeOfflineHours: number; catalogBehindDays: number; repeatHours: number };
  catalog: { githubRepo: string };
  agent: { latestVersion: string | null };
  vps: { stagingSuffix: string };
  checklist: { vpsPublicHostname: string };
  gateway: {
    defaults: {
      user: { rpm: number; concurrent: number; dailyTokens: number; expiresInDays: number };
      service: { rpm: number; concurrent: number; dailyTokens: number | null; expiresInDays: number | null };
    };
    maxUserKeyDays: number;
    timeouts: { connectMs: number; firstByteMs: number; idleMs: number; totalMs: number; nonStreamTotalMs: number };
    maxAttempts: number;
    queueFactor: number;
    corsOrigins: string[];
    usageRetentionDays: number;
    tailnetSuffix: string | null;
  };
};

/** A partial update: any subset of the keys, nested. */
export type LocalaiSettingsPatch = { [K in keyof LocalaiSettings]?: { [P in keyof LocalaiSettings[K]]?: LocalaiSettings[K][P] extends object ? Partial<LocalaiSettings[K][P]> : LocalaiSettings[K][P] } };

/** A Zod object of nested groups built from the table; each leaf is `leaf(def)`. */
function buildTreeSchema(leaf: (def: SettingDef) => z.ZodType): z.ZodType {
  interface Node {
    children: Map<string, Node>;
    def?: SettingDef;
  }
  const root: Node = { children: new Map() };
  for (const def of LOCALAI_SETTING_DEFS) {
    let node = root;
    for (const segment of def.key.split('.')) {
      if (!node.children.has(segment)) node.children.set(segment, { children: new Map() });
      node = node.children.get(segment)!;
    }
    node.def = def;
  }
  const build = (node: Node, atRoot: boolean): z.ZodType => {
    if (node.def && !atRoot) return leaf(node.def);
    const shape: Record<string, z.ZodType> = {};
    for (const [name, child] of node.children) shape[name] = build(child, false);
    return z.object(shape).strict();
  };
  return build(root, true);
}

const storedSchema = buildTreeSchema((def) => def.schema) as unknown as z.ZodType<LocalaiSettings>;
const patchSchema = buildTreeSchema((def) => def.schema.optional());

/** The defaults, nested. A fresh copy each call. */
export function localaiSettingsDefaults(): LocalaiSettings {
  return nest(Object.fromEntries(LOCALAI_SETTING_DEFS.map((def) => [def.key, def.default]))) as unknown as LocalaiSettings;
}

/**
 * Salvages a stored `localai` value, key by key: the nested value, else the dotted key, else the default. A value that
 * fails its schema reads as the default, so a damaged document can never make the lab unreadable or unsavable.
 */
export function readLocalaiSettings(stored: unknown): LocalaiSettings {
  const flat: Record<string, unknown> = {};
  for (const def of LOCALAI_SETTING_DEFS) {
    const nested = getAtKey(stored, def.key);
    const dotted = isTree(stored) ? stored[def.key] : undefined;
    const candidate = nested !== undefined ? nested : dotted;
    const parsed = candidate === undefined ? undefined : def.schema.safeParse(candidate);
    flat[def.key] = parsed?.success ? parsed.data : def.default;
  }
  return nest(flat) as unknown as LocalaiSettings;
}

/** PATCH merge: each key present in `patch` replaces the same key of `current`; nothing else changes. */
export function mergeLocalaiSettings(current: LocalaiSettings, patch: LocalaiSettingsPatch | undefined): LocalaiSettings {
  const next = structuredClone(current) as unknown as Tree;
  if (patch === undefined) return next as unknown as LocalaiSettings;
  for (const def of LOCALAI_SETTING_DEFS) {
    const value = getAtKey(patch, def.key);
    if (value !== undefined) setAtKey(next, def.key, structuredClone(value));
  }
  return next as unknown as LocalaiSettings;
}

export const LOCALAI_SYSTEM_SETTINGS = {
  key: LOCALAI_SETTINGS_NAMESPACE,
  description: 'Local AI lab settings: alert thresholds, catalog source, agent version, VPS publishing and the gateway (written only by PUT /api/localai/settings).',
  storedSchema,
  patchSchema,
  // Refused on the platform's own routes on purpose (see the header): the Local AI route checks the permission per prefix.
  putSchema: z.never().optional(),
  wirePatchSchema: z.never().optional(),
  responseSchema: storedSchema,
  defaults: localaiSettingsDefaults(),
  requiredOnPut: false,
  read: (stored: unknown, _helpers: SettingsReadHelpers) => readLocalaiSettings(stored),
  merge: mergeLocalaiSettings,
} satisfies SystemSettingsNamespace<'localai', LocalaiSettings, LocalaiSettingsPatch>;

// Types `SystemSettingsService.getNamespace('localai')` as `LocalaiSettings`.
declare module '@marinoscar/platform-api/settings' {
  interface SystemSettingsNamespaces {
    localai: LocalaiSettings;
  }
}

registerSystemSettingsNamespaces([LOCALAI_SYSTEM_SETTINGS as unknown as SystemSettingsNamespace]);
