// What the top bar shows for a status: pure functions, unit-tested (tests/unit/topbar-model.test.ts).
import type { DesktopStatus, Lang, NodeMode, Tab } from '../../shared/types';
import { STRINGS, pick } from './strings';

export type PillTone = 'ok' | 'busy' | 'bad' | 'warn';

export interface Pill {
  tone: PillTone;
  label: string;
}

export function pillFor(status: DesktopStatus | null, lang: Lang): Pill {
  if (!status || status.agent === 'starting') return { tone: 'busy', label: pick(STRINGS.pillStarting, lang) };
  if (status.agent === 'down') return { tone: 'bad', label: pick(STRINGS.pillDown, lang) };
  if (status.agent === 'unauthorized') return { tone: 'bad', label: pick(STRINGS.pillSignIn, lang) };
  if (status.alerts > 0) return { tone: 'warn', label: pick(STRINGS.pillAttention, lang) };
  return { tone: 'ok', label: pick(STRINGS.pillReady, lang) };
}

export function modeLabel(mode: NodeMode | null, lang: Lang): string | null {
  switch (mode) {
    case 'online':
      return pick(STRINGS.modeOnline, lang);
    case 'offline':
      return pick(STRINGS.modeOffline, lang);
    case 'survival':
      return pick(STRINGS.modeSurvival, lang);
    default:
      return null;
  }
}

/** The Lab tab exists only in connected mode (desktop.md 4.9). */
export function tabsFor(status: DesktopStatus | null): Tab[] {
  return status?.connected ? ['local', 'lab'] : ['local'];
}

/** The tab to mark as selected: never `lab` while the tab is not offered. */
export function selectedTab(status: DesktopStatus | null): Tab {
  return status?.connected && status.tab === 'lab' ? 'lab' : 'local';
}

/** The 32 px survival strip replaces the whole bar while the desktop shows the Portal in survival form (desktop.md 4.7). */
export function stripFor(status: DesktopStatus | null, lang: Lang): { active: boolean; label: string; fullScreenLabel: string; showBack: boolean; banner: string | null } {
  const s = status?.survival;
  const active = s?.active === true;
  return {
    active,
    // A Portal opened outside survival mode does not call itself "Survival mode".
    label: pick(s?.portalOnly === true ? STRINGS.survivalPortal : STRINGS.survivalMode, lang),
    fullScreenLabel: pick(s?.fullScreen === true ? STRINGS.exitFullScreen : STRINGS.enterFullScreen, lang),
    showBack: active && s?.portalOnly === true,
    banner: s?.banner ? `${s.banner.code} · ${s.banner.text}` : null,
  };
}
