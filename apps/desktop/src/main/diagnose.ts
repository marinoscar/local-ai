// `ai-desktop --diagnose`: a JSON doctor report on stdout, no window, exit code 0 (docs/specs/desktop.md 3.9, 7).
// Checks carry severities; a failing check never changes the exit code. Only loopback is touched: the agent.
import { spawnSync } from 'node:child_process';
import { existsSync, openSync, readSync, closeSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { app, safeStorage } from 'electron';
import { createAgentClient, checkApiVersion, resolveTokenPath, testHooksAllowed, type AgentClient } from './agent-client';
import { createAgentSupervisor, type AgentSupervisor } from './agent-supervisor';
import { autostartPath } from './autostart';
import { blockedTotal } from './egress-guard';
import { type DskCode, type Lang, type Severity, ERROR_COPY, resolveLang } from './i18n';
import { configDir, readSettings, type Settings } from './settings';
import { detectTray } from './tray';

export const MODULE = 'diagnose' as const;

export interface DiagnoseCheck {
  code: DskCode;
  severity: Severity;
  title: string;
  fix: string;
}

export interface DiagnoseReport {
  version: string;
  electron: string;
  agent: { reachable: boolean; version: string | null; unit_state: string };
  token: { readable: boolean; group_member: boolean | null; needs_relogin: boolean };
  /** `ok` the agent accepts the token, `refused` it answered 401, `skipped` it could not be asked. */
  session: 'ok' | 'refused' | 'skipped';
  tray: 'supported' | 'unsupported' | 'unknown';
  safe_storage_backend: string;
  cloud: { linked: boolean; reachable: boolean | null };
  egress_blocked_total: number;
  autostart: { enabled: boolean; file_present: boolean };
  checks: DiagnoseCheck[];
}

/** The AppArmor facts behind LAI-DSK-015. Every reader returns null when the answer cannot be read (not root). */
export interface ApparmorProbe {
  /** True when this is an installed (packaged) app: a development run is not judged. */
  applies: boolean;
  /** `/proc/sys/kernel/apparmor_restrict_unprivileged_userns`. */
  restrictUserns(): string | null;
  /** `/sys/kernel/security/apparmor/profiles` (root only on most systems). */
  loadedProfiles(): string | null;
  /** `/etc/apparmor.d/ai-desktop`, installed by the deb. */
  profileFilePresent(): boolean;
}

export function sandboxBlocked(probe: ApparmorProbe): boolean {
  if (!probe.applies) return false;
  if (probe.restrictUserns()?.trim() !== '1') return false;
  const loaded = probe.loadedProfiles();
  return loaded !== null ? !/(^|\s)ai-desktop(\s|$)/m.test(loaded) : !probe.profileFilePresent();
}

export interface DiagnoseDeps {
  version: string;
  electron: string;
  settings: Settings;
  lang: Lang;
  client: Pick<AgentClient, 'health' | 'readToken' | 'status'>;
  supervisor: Pick<AgentSupervisor, 'unitState' | 'diagnoseTokenAccess'>;
  safeStorageBackend(): string;
  tray(): DiagnoseReport['tray'] | Promise<DiagnoseReport['tray']>;
  egressBlockedTotal(): number;
  autostartFilePresent(): boolean;
  apparmor: ApparmorProbe;
}

function check(code: DskCode, lang: Lang): DiagnoseCheck {
  const copy = ERROR_COPY[code];
  return {
    code,
    severity: copy.severity,
    title: copy.title[lang],
    fix: copy.command === undefined ? copy.fix[lang] : `${copy.fix[lang]} ${copy.command}`,
  };
}

export async function buildReport(deps: DiagnoseDeps): Promise<DiagnoseReport> {
  const checks: DiagnoseCheck[] = [];
  const add = (code: DskCode): void => {
    checks.push(check(code, deps.lang));
  };

  const [health, unit_state, token, group] = await Promise.all([
    deps.client.health({ tries: 2 }),
    deps.supervisor.unitState(),
    deps.client.readToken(),
    deps.supervisor.diagnoseTokenAccess(),
  ]);

  const reachable = health.reachable;
  if (!health.reachable) add('LAI-DSK-004');
  const agentVersion = health.reachable ? health.health.version : null;
  const versionOk = health.reachable ? checkApiVersion(health.health.version) === 'ok' : false;
  if (health.reachable && !versionOk) add('LAI-DSK-006');

  const needsRelogin = !token.ok && token.reason === 'permission-denied' && group.kind === 'needs-relogin';
  if (!token.ok) {
    if (token.reason !== 'permission-denied') add('LAI-DSK-003');
    else add(needsRelogin ? 'LAI-DSK-002' : 'LAI-DSK-001');
  }

  let session: DiagnoseReport['session'] = 'skipped';
  if (token.ok && health.reachable && versionOk) {
    const status = await deps.client.status();
    if (status.ok) session = 'ok';
    else if (status.kind === 'unauthorized') {
      session = 'refused';
      add('LAI-DSK-007');
    }
  }

  if (sandboxBlocked(deps.apparmor)) add('LAI-DSK-015');

  return {
    version: deps.version,
    electron: deps.electron,
    agent: { reachable, version: agentVersion, unit_state },
    token: { readable: token.ok, group_member: group.kind !== 'not-member', needs_relogin: needsRelogin },
    session,
    tray: await deps.tray(),
    safe_storage_backend: deps.safeStorageBackend(),
    cloud: { linked: deps.settings.cloud.linked, reachable: null },
    egress_blocked_total: deps.egressBlockedTotal(),
    autostart: { enabled: deps.settings.autostart, file_present: deps.autostartFilePresent() },
    checks,
  };
}

/**
 * Blocks counted by the running app: the last `blocked_total` of an egress line in main.log (the count since that
 * process started). 0 when the log has none. Reads only the last 1 MB.
 */
export function egressTotalFromLog(logFile: string): number {
  try {
    const size = statSync(logFile).size;
    const length = Math.min(size, 1024 * 1024);
    const fd = openSync(logFile, 'r');
    try {
      const buffer = Buffer.alloc(length);
      readSync(fd, buffer, 0, length, size - length);
      const lines = buffer.toString('utf8').split('\n');
      for (let i = lines.length - 1; i >= 0; i -= 1) {
        const line = lines[i] ?? '';
        if (!line.includes('"component":"egress"')) continue;
        try {
          const total = (JSON.parse(line) as { blocked_total?: unknown }).blocked_total;
          if (typeof total === 'number') return total;
        } catch {
          // a cut first line of the window
        }
      }
    } finally {
      closeSync(fd);
    }
  } catch {
    // no log yet
  }
  return 0;
}

// ---------------------------------------------------------------------------------------------------------------------
// Headless start
// ---------------------------------------------------------------------------------------------------------------------

/**
 * Electron on Linux needs a display to start its UI platform, even when no window is ever opened. Without one the
 * report is produced by a second copy of this program in Chromium's headless mode.
 */
export function needsHeadlessReexec(env: NodeJS.ProcessEnv, platform: string): boolean {
  return platform === 'linux' && !env['DISPLAY'] && !env['WAYLAND_DISPLAY'] && env['LOCALAI_DESKTOP_DIAGNOSE_CHILD'] !== '1';
}

export function headlessArgs(argv: readonly string[]): string[] {
  // Chromium switches go before the application path and its own arguments.
  return ['--ozone-platform=headless', '--disable-gpu', ...argv.slice(1)];
}

function readOrNull(path: string): string | null {
  try {
    return readFileSync(path, 'utf8');
  } catch {
    return null;
  }
}

/** Runs from the main script, before `ready` and before the single-instance lock. Never returns a window. */
export function runDiagnoseMain(): void {
  if (needsHeadlessReexec(process.env, process.platform)) {
    const child = spawnSync(process.execPath, headlessArgs(process.argv), {
      stdio: 'inherit',
      env: { ...process.env, LOCALAI_DESKTOP_DIAGNOSE_CHILD: '1' },
    });
    app.exit(child.status ?? 1);
    return;
  }
  void app.whenReady().then(async () => {
    const dir = configDir();
    const settings = await readSettings(dir);
    const lang = resolveLang(settings.lang, app.getLocale());
    const client = createAgentClient({ tokenPath: resolveTokenPath(process.env, testHooksAllowed(app.isPackaged, process.execPath)) });
    const report = await buildReport({
      version: app.getVersion(),
      electron: process.versions.electron ?? '',
      settings,
      lang,
      client,
      supervisor: createAgentSupervisor(),
      safeStorageBackend: () => {
        try {
          return typeof safeStorage.getSelectedStorageBackend === 'function' ? safeStorage.getSelectedStorageBackend() : 'unknown';
        } catch {
          return 'unknown';
        }
      },
      tray: async () => {
        const availability = await detectTray();
        return availability === 'available' ? 'supported' : availability === 'unavailable' ? 'unsupported' : 'unknown';
      },
      egressBlockedTotal: () => Math.max(blockedTotal(), egressTotalFromLog(join(dir, 'logs', 'main.log'))),
      autostartFilePresent: () => existsSync(autostartPath()),
      apparmor: {
        applies: app.isPackaged,
        restrictUserns: () => readOrNull('/proc/sys/kernel/apparmor_restrict_unprivileged_userns'),
        loadedProfiles: () => readOrNull('/sys/kernel/security/apparmor/profiles'),
        profileFilePresent: () => existsSync('/etc/apparmor.d/ai-desktop'),
      },
    });
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`, () => app.exit(0));
  }).catch((error: unknown) => {
    process.stderr.write(`ai-desktop --diagnose failed: ${(error as Error).message}\n`);
    app.exit(1);
  });
}
