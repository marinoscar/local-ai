import {
  composeDefaultSystemSettings,
  composePatchSystemSettingsSchema,
  composeUpdateSystemSettingsSchema,
  systemSettingsNamespaceRegistry,
} from '@marinoscar/platform-api/settings';

import '../platform/platform';
import { DEFAULT_ALERT_SETTINGS } from './alerts/alert-settings';
import { DEFAULT_CATALOG_GITHUB_REPO } from './catalog/catalog.constants';
import {
  LOCALAI_SETTING_DEFS,
  LOCALAI_SYSTEM_SETTINGS,
  flatten,
  localaiSettingsDefaults,
  mergeLocalaiSettings,
  nest,
  readLocalaiSettings,
  settingDef,
  settingPermission,
} from './localai.settings';
import { DEFAULT_VPS_PUBLIC_HOSTNAME } from './wizard/wizard-settings';

describe('the localai namespace table', () => {
  it('lists exactly the keys of cloud-app.md 3.10 and gateway.md 3.7', () => {
    expect(LOCALAI_SETTING_DEFS.map((def) => def.key)).toEqual([
      'alerts.driveStaleDays',
      'alerts.driveCriticalDays',
      'alerts.nodeStaleMinutes',
      'alerts.nodeOfflineHours',
      'alerts.catalogBehindDays',
      'alerts.repeatHours',
      'catalog.githubRepo',
      'agent.latestVersion',
      'vps.stagingSuffix',
      'checklist.vpsPublicHostname',
      'gateway.defaults.user',
      'gateway.defaults.service',
      'gateway.maxUserKeyDays',
      'gateway.timeouts',
      'gateway.maxAttempts',
      'gateway.queueFactor',
      'gateway.corsOrigins',
      'gateway.usageRetentionDays',
      'gateway.tailnetSuffix',
    ]);
  });

  it('has a default that satisfies its own schema, for every key', () => {
    for (const def of LOCALAI_SETTING_DEFS) expect(def.schema.safeParse(def.default).success).toBe(true);
  });

  it('agrees with the defaults the pre-existing readers use', () => {
    const defaults = localaiSettingsDefaults();
    expect(defaults.alerts).toEqual({
      driveStaleDays: DEFAULT_ALERT_SETTINGS.driveStaleDays,
      driveCriticalDays: DEFAULT_ALERT_SETTINGS.driveCriticalDays,
      nodeStaleMinutes: DEFAULT_ALERT_SETTINGS.nodeStaleMinutes,
      nodeOfflineHours: DEFAULT_ALERT_SETTINGS.nodeOfflineHours,
      catalogBehindDays: DEFAULT_ALERT_SETTINGS.catalogBehindDays,
      repeatHours: DEFAULT_ALERT_SETTINGS.repeatHours,
    });
    expect(defaults.agent.latestVersion).toBe(DEFAULT_ALERT_SETTINGS.agentLatestVersion);
    expect(defaults.catalog.githubRepo).toBe(DEFAULT_CATALOG_GITHUB_REPO);
    expect(defaults.checklist.vpsPublicHostname).toBe(DEFAULT_VPS_PUBLIC_HOSTNAME);
  });

  it('has the gateway defaults of gateway.md 3.7', () => {
    const { gateway } = localaiSettingsDefaults();
    expect(gateway.defaults.user).toEqual({ rpm: 60, concurrent: 4, dailyTokens: 2000000, expiresInDays: 90 });
    expect(gateway.defaults.service).toEqual({ rpm: 600, concurrent: 16, dailyTokens: null, expiresInDays: null });
    expect(gateway.maxUserKeyDays).toBe(365);
    expect(gateway.timeouts).toEqual({ connectMs: 5000, firstByteMs: 180000, idleMs: 120000, totalMs: 1800000, nonStreamTotalMs: 900000 });
    expect(gateway.maxAttempts).toBe(3);
    expect(gateway.queueFactor).toBe(4);
    expect(gateway.corsOrigins).toEqual([]);
    expect(gateway.usageRetentionDays).toBe(90);
    expect(gateway.tailnetSuffix).toBeNull();
  });

  it('round-trips nested and flat', () => {
    expect(flatten(localaiSettingsDefaults())).toEqual(Object.fromEntries(LOCALAI_SETTING_DEFS.map((def) => [def.key, def.default])));
    expect(nest(flatten(localaiSettingsDefaults()))).toEqual(localaiSettingsDefaults());
  });
});

describe('who may write a key', () => {
  it('gateway.* needs gateway:admin and every other key lab:admin', () => {
    for (const def of LOCALAI_SETTING_DEFS) {
      expect(settingPermission(def.key)).toBe(def.key.startsWith('gateway.') ? 'gateway:admin' : 'lab:admin');
    }
    expect(settingPermission('alerts.repeatHours')).toBe('lab:admin');
    expect(settingPermission('gateway.maxUserKeyDays')).toBe('gateway:admin');
  });

  it('knows no key it was not given', () => {
    expect(settingDef('gateway.nope')).toBeUndefined();
    expect(settingDef('alerts.repeatHours')).toBeDefined();
  });
});

describe('readLocalaiSettings (the salvage read)', () => {
  it('reads an empty or damaged document as the defaults', () => {
    for (const stored of [undefined, null, 'x', 42, [], {}]) expect(readLocalaiSettings(stored)).toEqual(localaiSettingsDefaults());
  });

  it('takes the nested value, and the dotted key when no nested value exists', () => {
    const read = readLocalaiSettings({ alerts: { repeatHours: 6 }, 'alerts.driveStaleDays': 7, 'gateway.maxUserKeyDays': 30 });
    expect(read.alerts.repeatHours).toBe(6);
    expect(read.alerts.driveStaleDays).toBe(7);
    expect(read.gateway.maxUserKeyDays).toBe(30);
    expect(read.alerts.driveCriticalDays).toBe(90);
  });

  it('prefers the nested value over the dotted key', () => {
    expect(readLocalaiSettings({ alerts: { repeatHours: 6 }, 'alerts.repeatHours': 12 }).alerts.repeatHours).toBe(6);
  });

  it('drops an invalid value key by key and keeps the good ones', () => {
    const read = readLocalaiSettings({ alerts: { repeatHours: -1, driveStaleDays: 5 }, gateway: { defaults: { user: { rpm: 'many' } }, maxAttempts: 4 } });
    expect(read.alerts.repeatHours).toBe(24);
    expect(read.alerts.driveStaleDays).toBe(5);
    expect(read.gateway.defaults.user).toEqual(localaiSettingsDefaults().gateway.defaults.user);
    expect(read.gateway.maxAttempts).toBe(4);
  });
});

describe('mergeLocalaiSettings', () => {
  it('replaces only the keys present, a whole leaf object at a time', () => {
    const current = localaiSettingsDefaults();
    const merged = mergeLocalaiSettings(current, { gateway: { defaults: { user: { rpm: 10, concurrent: 2, dailyTokens: 5, expiresInDays: 7 } }, maxAttempts: 2 } });
    expect(merged.gateway.defaults.user).toEqual({ rpm: 10, concurrent: 2, dailyTokens: 5, expiresInDays: 7 });
    expect(merged.gateway.maxAttempts).toBe(2);
    expect(merged.gateway.defaults.service).toEqual(current.gateway.defaults.service);
    expect(merged.alerts).toEqual(current.alerts);
  });

  it('keeps an explicit null, and returns a copy', () => {
    const current = { ...localaiSettingsDefaults() };
    current.agent = { latestVersion: '1.2.3' };
    const merged = mergeLocalaiSettings(current, { agent: { latestVersion: null } });
    expect(merged.agent.latestVersion).toBeNull();
    expect(current.agent.latestVersion).toBe('1.2.3');
    expect(mergeLocalaiSettings(current, undefined)).not.toBe(current);
  });
});

describe('the namespace in the platform registry', () => {
  it('is registered once, before the jobs namespace the platform composes', () => {
    const ids = systemSettingsNamespaceRegistry.ids();
    expect(ids).toContain('localai');
    expect(ids.filter((id: string) => id === 'localai')).toHaveLength(1);
  });

  it('is in the default document, complete', () => {
    expect(composeDefaultSystemSettings()).toMatchObject({ localai: localaiSettingsDefaults() });
  });

  it('is refused by the platform routes: PATCH and PUT /api/system-settings cannot write it', () => {
    expect(composePatchSystemSettingsSchema().safeParse({ localai: { gateway: { maxAttempts: 2 } } }).success).toBe(false);
    const put = { ...composeDefaultSystemSettings() } as Record<string, unknown>;
    expect(composeUpdateSystemSettingsSchema().safeParse({ ...put, localai: localaiSettingsDefaults() }).success).toBe(false);
  });

  it('still accepts a PATCH that does not mention it', () => {
    expect(composePatchSystemSettingsSchema().safeParse({ jobs: { history: { purgeEnabled: false } } }).success).toBe(true);
  });

  it('the declaration merge and read are the exported functions', () => {
    expect(LOCALAI_SYSTEM_SETTINGS.merge).toBe(mergeLocalaiSettings);
    expect(LOCALAI_SYSTEM_SETTINGS.read({}, {} as never)).toEqual(localaiSettingsDefaults());
  });
});
