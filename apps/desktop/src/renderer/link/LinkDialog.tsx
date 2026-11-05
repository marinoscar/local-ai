// The device-link dialog (docs/specs/desktop.md 4.8): a big code, "Open the browser", a countdown, the errors. It talks to
// the main process only through `window.localaiDesktop.cloud`; the token never reaches this page.
import { Box, Button, CircularProgress, Stack, Typography } from '@mui/material';
import { type ReactElement, useCallback, useEffect, useRef, useState } from 'react';
import type { CloudLinkState, Lang, LinkBegin, LinkRefusal, LocalaiDesktop } from '../../shared/types';
import { linkView } from './model';
import { STRINGS, pick } from './strings';

export interface LinkDialogProps {
  bridge?: LocalaiDesktop;
  lang: Lang;
}

export function LinkDialog({ bridge, lang }: LinkDialogProps): ReactElement {
  const [begin, setBegin] = useState<LinkBegin | LinkRefusal | null>(null);
  const [cloud, setCloud] = useState<CloudLinkState | null>(null);
  const [secondsLeft, setSecondsLeft] = useState(0);
  const deadline = useRef(0);

  const start = useCallback(async () => {
    if (!bridge) return;
    setBegin(null);
    try {
      const answer = await bridge.cloud.beginLink();
      if ('userCode' in answer) {
        deadline.current = Date.now() + answer.expiresIn * 1000;
        setSecondsLeft(answer.expiresIn);
      }
      setBegin(answer);
    } catch {
      setBegin({ ok: false, code: 'unavailable' });
    }
  }, [bridge]);

  // The dialog is its own window, which the main process does not push status to (only the top bar, the Lab and the
  // error page get that): it asks for the link state every second instead, which also drives the countdown.
  useEffect(() => {
    void start();
    const timer = setInterval(() => {
      if (deadline.current > 0) setSecondsLeft(Math.max(0, Math.ceil((deadline.current - Date.now()) / 1000)));
      void bridge?.cloud.linkState().then(setCloud, () => undefined);
    }, 1000);
    void bridge?.cloud.linkState().then(setCloud, () => undefined);
    return () => clearInterval(timer);
  }, [bridge, start]);

  const view = linkView({ begin, cloud, secondsLeft, lang });
  const busy = view.kind === 'loading' || view.kind === 'saving';
  return (
    <Box component="main" sx={{ p: 3, display: 'flex', flexDirection: 'column', gap: 2, minHeight: '100vh', boxSizing: 'border-box' }}>
      <Typography component="h1" variant="h5" sx={{ fontWeight: 700 }}>
        {pick(STRINGS.title, lang)}
      </Typography>
      <Typography role="status" aria-live="polite" data-testid="link-message">
        {view.message}
      </Typography>
      {view.userCode !== undefined && (
        <Box sx={{ my: 1, textAlign: 'center' }}>
          <Typography variant="caption" color="text.secondary">
            {pick(STRINGS.codeLabel, lang)}
          </Typography>
          <Typography component="p" data-testid="link-code" aria-label={view.userCode.split('').join(' ')} sx={{ fontSize: '3rem', fontWeight: 700, letterSpacing: '0.15em', fontFamily: 'ui-monospace, monospace', userSelect: 'all' }}>
            {view.userCode}
          </Typography>
        </Box>
      )}
      {view.countdown !== undefined && (
        <Typography variant="body2" color="text.secondary" data-testid="link-countdown">
          {view.countdown}
        </Typography>
      )}
      {view.detail !== undefined && (
        <Typography variant="body2" color="text.secondary" data-testid="link-detail">
          {view.detail}
        </Typography>
      )}
      {busy && <CircularProgress size={28} aria-label={view.message} />}
      <Box sx={{ flexGrow: 1 }} />
      <Stack direction="row" spacing={1} sx={{ justifyContent: 'flex-end' }}>
        {view.canOpen && (
          <Button variant="contained" onClick={() => bridge?.cloud.openVerification()} data-testid="link-open">
            {pick(STRINGS.openBrowser, lang)}
          </Button>
        )}
        {view.canRetry && (
          <Button variant="contained" onClick={() => void start()} data-testid="link-retry">
            {pick(STRINGS.tryAgain, lang)}
          </Button>
        )}
        <Button variant="outlined" onClick={() => window.close()} data-testid="link-close">
          {pick(view.kind === 'done' ? STRINGS.close : STRINGS.cancel, lang)}
        </Button>
      </Stack>
    </Box>
  );
}
