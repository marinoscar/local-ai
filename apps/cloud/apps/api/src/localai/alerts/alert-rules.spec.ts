// Every row of the kind table (docs/specs/cloud-app.md section 4.9): a raise test, a clear test and the boundary, all at
// a fixed `now`. Pure: no database, no clock.
import { evaluateNodeAlerts, describeDuration, type ActiveAlertView } from './alert-rules';
import { DAY, HOUR, MIN, NOW, ago, context, node, settings, state } from './alert-fixtures';

const kinds = (...args: Parameters<typeof evaluateNodeAlerts>) => evaluateNodeAlerts(...args).map((alert) => alert.kind);
const find = (kind: string, ...args: Parameters<typeof evaluateNodeAlerts>) => evaluateNodeAlerts(...args).find((alert) => alert.kind === kind);
const activeOf = (kind: string, extra: Partial<ActiveAlertView> = {}): Map<string, ActiveAlertView> =>
  new Map([[kind, { kind, code: 'LAI-CLD-020', severity: 'warning', title: 'held', details: null, ...extra }]]);

describe('a healthy node', () => {
  it('raises nothing', () => {
    expect(evaluateNodeAlerts(node(), context())).toEqual([]);
  });

  it('a node that is not enrolled raises nothing at all, whatever its row says', () => {
    expect(evaluateNodeAlerts(node({ phase: 'pending', mode: null, lastStateAt: null, lastState: null, keyReportedAt: null }), context())).toEqual([]);
  });
});

describe('node.stale (LAI-CLD-020)', () => {
  const silent = (ms: number) => node({ lastStateAt: ago(ms), lastContactAt: ago(ms), eab: 'stale' });

  it('is not raised before alerts.nodeStaleMinutes of silence (degraded for 9 min 59 s)', () => {
    expect(kinds(silent(10 * MIN - 1000), context())).not.toContain('node.stale');
  });

  it('is raised at exactly nodeStaleMinutes (the boundary), as a warning', () => {
    const alert = find('node.stale', silent(10 * MIN), context());
    expect(alert).toMatchObject({ kind: 'node.stale', code: 'LAI-CLD-020', severity: 'warning', title: 'Not seen for 10 min' });
  });

  it('follows the setting', () => {
    const ctx = context({ settings: settings({ nodeStaleMinutes: 30 }) });
    expect(kinds(silent(20 * MIN), ctx)).not.toContain('node.stale');
    expect(kinds(silent(30 * MIN), ctx)).toContain('node.stale');
  });

  it('is raised for a node that is down too, and names the silence', () => {
    expect(find('node.stale', node({ lastStateAt: ago(3 * HOUR), lastContactAt: ago(3 * HOUR), eab: 'stale' }), context())?.title).toBe('Not seen for 3 h');
  });

  it('is cleared when the node is healthy again (a fresh snapshot and a healthy heartbeat)', () => {
    expect(kinds(node(), context(), activeOf('node.stale'))).not.toContain('node.stale');
  });

  it('HYSTERESIS: stays active while the node is only degraded again, but is not raised afresh', () => {
    const degraded = node({ lastStateAt: ago(4 * MIN), lastContactAt: ago(4 * MIN), eab: 'stale' });
    expect(kinds(degraded, context())).not.toContain('node.stale');
    expect(kinds(degraded, context(), activeOf('node.stale'))).toContain('node.stale');
  });

  it('EXPECTED SILENCE: a node in offline or survival mode raises nothing and clears the alert', () => {
    for (const mode of ['offline', 'survival'] as const) {
      const quiet = node({ mode, lastState: state({ mode }), lastStateAt: ago(3 * HOUR), lastContactAt: ago(3 * HOUR), eab: 'offline' });
      expect(kinds(quiet, context())).not.toContain('node.stale');
      expect(kinds(quiet, context(), activeOf('node.stale'))).not.toContain('node.stale');
    }
  });

  it('is never raised for a node that has not reported a mode', () => {
    expect(kinds(node({ mode: null, lastStateAt: null, lastContactAt: null, eab: 'offline' }), context())).not.toContain('node.stale');
  });
});

describe('node.offline (LAI-CLD-021)', () => {
  const offline = (ms: number) => node({ lastStateAt: ago(ms), lastContactAt: ago(ms), eab: 'offline' });

  it('is not raised just under alerts.nodeOfflineHours (23 h 59 min)', () => {
    expect(kinds(offline(24 * HOUR - MIN), context())).not.toContain('node.offline');
  });

  it('is raised at exactly 24 hours, as critical', () => {
    expect(find('node.offline', offline(24 * HOUR), context())).toMatchObject({ code: 'LAI-CLD-021', severity: 'critical', title: 'Offline for 24 h' });
  });

  it('names days past 48 hours', () => {
    expect(find('node.offline', offline(3 * DAY), context())?.title).toBe('Offline for 3 days');
  });

  it('is held while the heartbeat is still missing, even under the threshold (hysteresis)', () => {
    expect(kinds(offline(2 * HOUR), context(), activeOf('node.offline', { severity: 'critical', code: 'LAI-CLD-021' }))).toContain('node.offline');
  });

  it('is cleared when the heartbeat resumes', () => {
    expect(kinds(node({ eab: 'healthy' }), context(), activeOf('node.offline'))).not.toContain('node.offline');
  });

  it('EXPECTED SILENCE: an offline-mode node is never reported offline', () => {
    expect(kinds(node({ mode: 'offline', lastState: state({ mode: 'offline' }), lastStateAt: ago(5 * DAY), lastContactAt: ago(5 * DAY), eab: 'offline' }), context())).not.toContain('node.offline');
  });

  it('a node with no sign of life at all raises nothing (nothing to measure the silence from)', () => {
    expect(kinds(node({ lastStateAt: null, lastContactAt: null, eab: 'offline' }), context())).not.toContain('node.offline');
  });
});

describe('ark.drive_stale (LAI-CLD-022)', () => {
  const withDrive = (daysAgo: number | null, extraRoots: unknown[] = []) =>
    node({
      lastState: state({
        ark: {
          roots: [{ id: 'internal', tiers: { t1: 'ok', t2: 'ok' } }, ...extraRoots],
          drive_last_synced_at: daysAgo === null ? null : ago(daysAgo * DAY + 3 * HOUR).toISOString(),
        },
      }),
    });

  it('is not raised at 30 whole days (30 d 3 h is still 30 days)', () => {
    expect(kinds(withDrive(30), context())).not.toContain('ark.drive_stale');
  });

  it('is a warning from the 31st day', () => {
    expect(find('ark.drive_stale', withDrive(31), context())).toMatchObject({ code: 'LAI-CLD-022', severity: 'warning', title: 'Ark drive not synced for 31 days' });
  });

  it('is a warning at 41 days and at exactly 90 days', () => {
    expect(find('ark.drive_stale', withDrive(41), context())?.severity).toBe('warning');
    expect(find('ark.drive_stale', withDrive(90), context())?.severity).toBe('warning');
  });

  it('ESCALATES to critical after alerts.driveCriticalDays: 91 and 95 days', () => {
    expect(find('ark.drive_stale', withDrive(91), context())?.severity).toBe('critical');
    expect(find('ark.drive_stale', withDrive(95), context())).toMatchObject({ severity: 'critical', title: 'Ark drive not synced for 95 days' });
  });

  it('follows both settings', () => {
    const ctx = context({ settings: settings({ driveStaleDays: 7, driveCriticalDays: 14 }) });
    expect(find('ark.drive_stale', withDrive(8), ctx)?.severity).toBe('warning');
    expect(find('ark.drive_stale', withDrive(15), ctx)?.severity).toBe('critical');
  });

  it('is cleared by a newer drive sync (the age falls back under the threshold)', () => {
    expect(kinds(withDrive(2), context(), activeOf('ark.drive_stale'))).not.toContain('ark.drive_stale');
  });

  it('a node that never synced a drive but reported a drive root is a warning', () => {
    expect(find('ark.drive_stale', withDrive(null, [{ id: 'ark-1a2b3c4d', present: false, tiers: { t1: 'ok', t2: 'ok' } }]), context())).toMatchObject({ severity: 'warning', title: 'Ark drive has never been synced' });
  });

  it('a node that never reported a drive root is not expected to have a drive', () => {
    expect(kinds(withDrive(null), context())).not.toContain('ark.drive_stale');
  });

  it('is raised even for a node in offline mode: the ark readiness does not depend on being online', () => {
    const quiet = node({ mode: 'offline', lastStateAt: ago(2 * DAY), lastContactAt: ago(2 * DAY), eab: 'offline', lastState: state({ mode: 'offline', ark: { roots: [], drive_last_synced_at: ago(50 * DAY).toISOString() } }) });
    expect(kinds(quiet, context())).toContain('ark.drive_stale');
  });
});

describe('sync.failed (LAI-CLD-023)', () => {
  const lastRun = (result: string, error_code: string | null = null) => node({ lastState: state({ sync: { running: false, last_run: { id: 'run-9', finished_at: NOW.toISOString(), result, skip_reason: null, error_code } } }) });

  it('is raised, as a warning, when the last run failed, and shows the node\'s own code', () => {
    expect(find('sync.failed', lastRun('failed', 'LAI-SYN-031'), context())).toMatchObject({ code: 'LAI-CLD-023', severity: 'warning', title: 'Last ark sync failed (LAI-SYN-031)' });
  });

  it('works without an error code', () => {
    expect(find('sync.failed', lastRun('failed'), context())?.title).toBe('Last ark sync failed');
  });

  it('ignores an error code that is not shaped like a doctor code', () => {
    expect(find('sync.failed', lastRun('failed', 'rm -rf /'), context())?.title).toBe('Last ark sync failed');
  });

  it('is cleared by the next run: ok or partial', () => {
    for (const result of ['ok', 'partial']) expect(kinds(lastRun(result), context(), activeOf('sync.failed'))).not.toContain('sync.failed');
  });

  it('is HELD through a skipped run (a skip proves nothing), and not raised by one', () => {
    expect(kinds(lastRun('skipped'), context(), activeOf('sync.failed'))).toContain('sync.failed');
    expect(kinds(lastRun('skipped'), context())).not.toContain('sync.failed');
  });

  it('a node that never synced raises nothing', () => {
    expect(kinds(node({ lastState: state({ sync: { running: false, last_run: null } }) }), context())).not.toContain('sync.failed');
  });
});

describe('drill.failed (LAI-CLD-024)', () => {
  const drill = (result: string, failed_checks: string[] = []) => node({ lastState: state({ drill: { run_id: 'd', last_run_at: NOW.toISOString(), result, survival_ready: false, failed_checks, failed_check_ids: [] } }) });

  it('a failed drill is critical and names its first failed check', () => {
    expect(find('drill.failed', drill('fail', ['LAI-DRL-020']), context())).toMatchObject({ code: 'LAI-CLD-024', severity: 'critical', title: 'Monthly drill failed: LAI-DRL-020' });
  });

  it('a warned drill is a warning', () => {
    expect(find('drill.failed', drill('warn'), context())).toMatchObject({ severity: 'warning', title: 'Monthly drill finished with warnings' });
  });

  it('is cleared by the next drill pass, and a node with no drill yet raises nothing', () => {
    expect(kinds(drill('pass'), context(), activeOf('drill.failed'))).not.toContain('drill.failed');
    expect(kinds(node({ lastState: state({ drill: null }) }), context())).not.toContain('drill.failed');
  });
});

describe('ark.tier1_missing (LAI-CLD-025)', () => {
  const roots = (...t1: string[]) => node({ lastState: state({ ark: { roots: t1.map((value, index) => ({ id: index === 0 ? 'internal' : `ark-0000000${index}`, tiers: { t1: value, t2: 'ok' } })), drive_last_synced_at: ago(DAY).toISOString() } }) });

  it('is critical when any root has tier 1 partial or missing', () => {
    expect(find('ark.tier1_missing', roots('ok', 'partial'), context())).toMatchObject({ code: 'LAI-CLD-025', severity: 'critical', title: 'Essential ark content is missing on ark-00000001' });
    expect(kinds(roots('missing'), context())).toContain('ark.tier1_missing');
  });

  it('is cleared when tier 1 is ok everywhere', () => {
    expect(kinds(roots('ok', 'ok'), context(), activeOf('ark.tier1_missing'))).not.toContain('ark.tier1_missing');
  });
});

describe('key.missing (LAI-CLD-026)', () => {
  it('is not raised before 10 minutes after enrolment (9 min 59 s)', () => {
    expect(kinds(node({ keyReportedAt: null, enrolledAt: ago(10 * MIN - 1000) }), context())).not.toContain('key.missing');
  });

  it('is a warning from exactly 10 minutes', () => {
    expect(find('key.missing', node({ keyReportedAt: null, enrolledAt: ago(10 * MIN) }), context())).toMatchObject({ code: 'LAI-CLD-026', severity: 'warning' });
  });

  it('is cleared once the key is reported, and never raised when the enrolment time is unknown', () => {
    expect(kinds(node({ keyReportedAt: ago(MIN), enrolledAt: ago(DAY) }), context(), activeOf('key.missing'))).not.toContain('key.missing');
    expect(kinds(node({ keyReportedAt: null, enrolledAt: null }), context())).not.toContain('key.missing');
  });
});

describe('catalog.behind (LAI-CLD-028)', () => {
  const approved = (daysAgo: number) => ({ version: '2026.11.02-1', approvedAt: ago(daysAgo * DAY) });

  it('is an info alert once the node has missed the approved catalog for alerts.catalogBehindDays', () => {
    expect(find('catalog.behind', node({ catalogVersion: '2026.10.28-2' }), context({ approvedCatalog: approved(14) }))).toMatchObject({ code: 'LAI-CLD-028', severity: 'info' });
  });

  it('is not raised a minute before 14 days', () => {
    expect(kinds(node({ catalogVersion: '2026.10.28-2' }), context({ approvedCatalog: { version: '2026.11.02-1', approvedAt: ago(14 * DAY - MIN) } }))).not.toContain('catalog.behind');
  });

  it('is cleared when the node matches, and never raised for a pinned node, no approved catalog or an unreported one', () => {
    const ctx = context({ approvedCatalog: approved(30) });
    expect(kinds(node({ catalogVersion: '2026.11.02-1' }), ctx, activeOf('catalog.behind'))).not.toContain('catalog.behind');
    expect(kinds(node({ catalogVersion: '2026.10.28-2', catalogPin: '2026.10.28-2' }), ctx)).not.toContain('catalog.behind');
    expect(kinds(node({ catalogVersion: '2026.10.28-2' }), context())).not.toContain('catalog.behind');
    expect(kinds(node({ catalogVersion: null }), ctx)).not.toContain('catalog.behind');
  });
});

describe('agent.outdated (LAI-CLD-029)', () => {
  const latest = (agentLatestVersion: string | null) => context({ settings: settings({ agentLatestVersion }) });

  it('is an info alert when the agent is older than agent.latestVersion', () => {
    expect(find('agent.outdated', node({ agentVersion: '0.4.0' }), latest('0.4.1'))).toMatchObject({ code: 'LAI-CLD-029', severity: 'info', title: 'Agent 0.4.0 is behind 0.4.1' });
  });

  it('is cleared when equal or newer, and never raised without a latest version or with an unreadable one', () => {
    expect(kinds(node({ agentVersion: '0.4.1' }), latest('0.4.1'), activeOf('agent.outdated'))).not.toContain('agent.outdated');
    expect(kinds(node({ agentVersion: '0.5.0' }), latest('0.4.1'))).not.toContain('agent.outdated');
    expect(kinds(node({ agentVersion: '0.4.0' }), latest(null))).not.toContain('agent.outdated');
    expect(kinds(node({ agentVersion: 'dev' }), latest('0.4.1'))).not.toContain('agent.outdated');
  });
});

describe('node.alerts.<code>: the node\'s own critical alerts', () => {
  const reported = (...alerts: { code: string; severity: string; title: string }[]) => node({ lastState: state({ alerts }) });

  it('one alert per critical code, with the node\'s code and title', () => {
    const result = evaluateNodeAlerts(reported({ code: 'LAI-HW-020', severity: 'critical', title: 'The laptop will sleep when the lid is closed' }, { code: 'LAI-HW-001', severity: 'critical', title: 'Second' }), context());
    expect(result.map((alert) => [alert.kind, alert.code, alert.severity])).toEqual([
      ['node.alerts.LAI-HW-020', 'LAI-HW-020', 'critical'],
      ['node.alerts.LAI-HW-001', 'LAI-HW-001', 'critical'],
    ]);
    expect(result[0]!.title).toBe('The laptop will sleep when the lid is closed');
  });

  it('ignores warning and info alerts, malformed codes, and repeats of the same code', () => {
    const result = evaluateNodeAlerts(
      reported(
        { code: 'LAI-HW-020', severity: 'warning', title: 'w' },
        { code: 'LAI-HW-001', severity: 'info', title: 'i' },
        { code: 'not-a-code', severity: 'critical', title: 'x' },
        { code: 'LAI-HW-002', severity: 'critical', title: 'first' },
        { code: 'LAI-HW-002', severity: 'critical', title: 'again' },
      ),
      context(),
    );
    expect(result.map((alert) => alert.kind)).toEqual(['node.alerts.LAI-HW-002']);
    expect(result[0]!.title).toBe('first');
  });

  it('is cleared when the code is gone from the snapshot', () => {
    expect(kinds(reported(), context())).toEqual([]);
  });
});

describe('describeDuration', () => {
  it('rounds down and never says 0', () => {
    expect(describeDuration(5)).toBe('1 min');
    expect(describeDuration(59 * 60)).toBe('59 min');
    expect(describeDuration(3600)).toBe('1 h');
    expect(describeDuration(47 * 3600)).toBe('47 h');
    expect(describeDuration(48 * 3600)).toBe('2 days');
    expect(describeDuration(Number.POSITIVE_INFINITY)).toBe('a long time');
  });
});

describe('the evaluation instant', () => {
  it('the fixtures use the fixed NOW', () => {
    expect(NOW.toISOString()).toBe('2026-11-04T12:00:00.000Z');
  });
});
