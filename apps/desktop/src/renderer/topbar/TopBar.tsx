// The top bar (docs/specs/desktop.md 3.4): 48 px, always visible. Tabs "This computer" | "Lab" (Lab only when
// connected), the status pill and mode chip, the three window buttons, Copy API key and the menu. It talks to the main
// process only through `window.localaiDesktop`; buttons whose feature has not landed show "Not available yet.".
import { Box, Button, Chip, IconButton, Tab, Tabs, ToggleButton, ToggleButtonGroup, Typography, useMediaQuery } from '@mui/material';
import { type ReactElement, useCallback, useEffect, useRef, useState } from 'react';
import type { ChildWindowName, DesktopStatus, Lang, LocalaiDesktop, Tab as TabName } from '../../shared/types';
import { modeLabel, pillFor, selectedTab, stripFor, tabsFor } from './model';
import { STRINGS, copiedText, copyFailedText, pick } from './strings';

const NOTICE_MS = 7000;
const PILL_COLOUR = { ok: 'success', busy: 'default', bad: 'error', warn: 'warning' } as const;

export interface TopBarProps {
  /** The shell bridge; absent in a plain browser (the bar then shows its starting state and the buttons do nothing). */
  bridge?: LocalaiDesktop;
}

export function TopBar({ bridge }: TopBarProps): ReactElement {
  const [status, setStatus] = useState<DesktopStatus | null>(null);
  const [lang, setLang] = useState<Lang>(() => bridge?.lang() ?? 'en');
  const [notice, setNotice] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const compact = useMediaQuery('(max-width:1100px)');

  const say = useCallback((text: string) => {
    setNotice(text);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setNotice(null), NOTICE_MS);
  }, []);

  useEffect(() => {
    if (!bridge) return undefined;
    let alive = true;
    const take = (next: DesktopStatus): void => {
      if (!alive) return;
      setStatus(next);
      setLang(bridge.lang());
    };
    void bridge.status().then(take, () => undefined);
    const offStatus = bridge.onStatus(take);
    const offNotice = bridge.onNotice((n) => say(n.text));
    return () => {
      alive = false;
      offStatus();
      offNotice();
      if (timer.current) clearTimeout(timer.current);
    };
  }, [bridge, say]);

  const open = (which: ChildWindowName) => () => bridge?.openWindow(which);
  const copyKey = async (): Promise<void> => {
    if (!bridge) return;
    try {
      const result = await bridge.copyApiKey();
      if ('code' in result && result.code === 'not_implemented') say(pick(STRINGS.notAvailable, lang));
      else if (result.ok) say(copiedText(result.clearsInS, lang));
      else say(copyFailedText(result.code, lang));
    } catch {
      say(pick(STRINGS.copyFailed, lang));
    }
  };

  const strip = stripFor(status, lang);
  if (strip.active) {
    const setLang = (next: Lang | null): void => {
      if (next !== null && next !== lang) void bridge?.settings.set({ lang: next }).catch(() => undefined);
    };
    return (
      <Box
        component="header"
        data-testid="survival-strip"
        sx={{ height: 32, display: 'flex', alignItems: 'center', gap: 1.5, px: 1.5, overflow: 'hidden', borderBottom: 1, borderColor: 'divider', bgcolor: 'warning.light', color: 'common.black' }}
      >
        <Typography component="h1" variant="body2" noWrap sx={{ fontWeight: 700, flexShrink: 0 }} data-testid="survival-label">
          {strip.label}
        </Typography>
        <Typography role={strip.banner ? 'alert' : undefined} variant="caption" noWrap data-testid="survival-banner" sx={{ flexGrow: 1, minWidth: 0 }}>
          {strip.banner ?? ''}
        </Typography>
        <ToggleButtonGroup
          exclusive
          size="small"
          value={lang}
          onChange={(_event, value: Lang | null) => setLang(value)}
          aria-label={pick(STRINGS.language, lang)}
          sx={{ height: 24, flexShrink: 0, '& .MuiToggleButton-root': { py: 0, px: 1, fontSize: '0.75rem', color: 'inherit', borderColor: 'currentColor' } }}
        >
          <ToggleButton value="en" data-testid="lang-en" aria-label="English" lang="en">
            EN
          </ToggleButton>
          <ToggleButton value="es" data-testid="lang-es" aria-label="Español" lang="es">
            ES
          </ToggleButton>
        </ToggleButtonGroup>
        <Button size="small" variant="outlined" color="inherit" onClick={() => bridge?.setFullScreen(status?.survival?.fullScreen !== true)} data-testid="exit-fullscreen" sx={{ py: 0, minHeight: 24, whiteSpace: 'nowrap', flexShrink: 0, fontSize: '0.75rem' }}>
          {strip.fullScreenLabel}
        </Button>
        {strip.showBack ? (
          <Button size="small" variant="outlined" color="inherit" onClick={() => bridge?.closePortal()} data-testid="survival-back" sx={{ py: 0, minHeight: 24, whiteSpace: 'nowrap', flexShrink: 0, fontSize: '0.75rem' }}>
            {pick(STRINGS.backToDashboard, lang)}
          </Button>
        ) : null}
      </Box>
    );
  }

  const pill = pillFor(status, lang);
  const mode = modeLabel(status?.mode ?? null, lang);
  const tabs = tabsFor(status);
  const button = { size: 'small', color: 'inherit', sx: { minWidth: 0, px: compact ? 0.75 : 1.25, whiteSpace: 'nowrap', fontSize: compact ? '0.8125rem' : undefined } } as const;

  return (
    <Box component="header" sx={{ height: 48, display: 'flex', alignItems: 'center', gap: 1, px: 1, overflow: 'hidden', borderBottom: 1, borderColor: 'divider', bgcolor: 'background.paper' }}>
      <Tabs
        value={selectedTab(status)}
        onChange={(_event, value: TabName) => bridge?.showTab(value)}
        aria-label={pick(STRINGS.tabs, lang)}
        sx={{ minHeight: 48, flexShrink: 0, '& .MuiTab-root': { minHeight: 48, minWidth: 0, px: compact ? 1.25 : 2, textTransform: 'none' } }}
      >
        {tabs.map((tab) => (
          <Tab key={tab} value={tab} label={pick(tab === 'local' ? STRINGS.tabLocal : STRINGS.tabLab, lang)} data-testid={`tab-${tab}`} />
        ))}
      </Tabs>
      <Box sx={{ flexGrow: 1, minWidth: 0 }}>
        <Typography role="status" aria-live="polite" variant="body2" noWrap data-testid="notice" sx={{ color: 'text.secondary' }}>
          {notice ?? ''}
        </Typography>
      </Box>
      <Chip
        size="small"
        color={PILL_COLOUR[pill.tone]}
        label={compact ? '' : pill.label}
        aria-label={pill.label}
        title={pill.label}
        data-testid="pill"
        data-tone={pill.tone}
        sx={compact ? { '& .MuiChip-label': { px: 0.75 }, minWidth: 24 } : undefined}
      />
      {mode ? <Chip size="small" variant="outlined" label={mode} data-testid="mode" /> : null}
      <Button {...button} onClick={open('chat')} data-testid="open-chat">
        {pick(STRINGS.chat, lang)}
      </Button>
      <Button {...button} onClick={open('library')} data-testid="open-library">
        {pick(STRINGS.library, lang)}
      </Button>
      <Button {...button} onClick={open('maps')} data-testid="open-maps">
        {pick(STRINGS.maps, lang)}
      </Button>
      <Button {...button} variant="outlined" onClick={() => void copyKey()} data-testid="copy-key">
        {pick(compact ? STRINGS.copyKeyShort : STRINGS.copyKey, lang)}
      </Button>
      <IconButton size="small" aria-label={pick(STRINGS.menu, lang)} aria-haspopup="menu" onClick={() => bridge?.showMenu()} data-testid="menu">
        <span aria-hidden="true">⋮</span>
      </IconButton>
    </Box>
  );
}
