// The ensure window (issue #480, docs/specs/desktop.md 4.12): "Install what's missing". State first (Complete, Installing n of m,
// Needs you), then the one next thing to do, one button, progress with a stop button, and each part in plain words.
// Everything comes from the main process through `window.localaiDesktop.ensure`; the page never calls the agent itself. Compiling
// and restarting a running service are never started by the main button: they open a confirmation that says what they cost, and
// approving asks for the administrator PIN (the same native window as Rotate key).
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, LinearProgress, List, ListItem, ListItemText, Stack, Typography } from '@mui/material';
import { type ReactElement, useCallback, useEffect, useRef, useState } from 'react';
import { type EnsureResult, type EnsureSnapshot, type Lang, type LocalaiDesktop } from '../../shared/types';
import { type Problem, STATE_MARK, STRINGS, canInstall, describeFailure, pick, pollDelay, progressText, stateSeverity, stateText, statusText, stepsNeedingOk, warningFor } from './model';

/** Read by a screen reader, not drawn: the state's name goes before the agent's sentence. */
const HIDDEN = { position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap' } as const;

type Bridge = Pick<LocalaiDesktop, 'lang' | 'ensure'>;

function ProblemAlert({ problem, lang }: { problem: Problem; lang: Lang }): ReactElement {
  return (
    <Alert severity="error" role="alert" data-testid="problem">
      <strong>{problem.text}</strong>
      <br />
      {pick(STRINGS.fix, lang)}: {problem.fix}
      {problem.code ? (
        <Typography component="div" variant="caption" data-testid="problem-code">
          {problem.code}
        </Typography>
      ) : null}
    </Alert>
  );
}

export function EnsurePage({ bridge }: { bridge?: Bridge }): ReactElement {
  const lang: Lang = bridge?.lang() ?? 'en';
  const [view, setView] = useState<EnsureSnapshot | null>(null);
  const [problem, setProblem] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const alive = useRef(true);

  const schedule = useCallback((next: EnsureSnapshot | null, poll: () => void) => {
    if (timer.current) clearTimeout(timer.current);
    if (alive.current) timer.current = setTimeout(poll, pollDelay(next));
  }, []);

  const accept = useCallback((result: EnsureResult): void => {
    if (result.ok) setView(result.view);
  }, []);

  const poll = useCallback(async (): Promise<void> => {
    if (!bridge) return;
    let next: EnsureSnapshot | null = null;
    try {
      const result = await bridge.ensure.view();
      if (!alive.current) return;
      if (result.ok) {
        next = result.view;
        setView(result.view);
        setProblem(null);
      } else setProblem(describeFailure(result, lang, 'view'));
    } catch {
      if (alive.current) setProblem(describeFailure({ ok: false, code: 'unavailable' }, lang, 'view'));
    }
    schedule(next, () => void poll());
  }, [bridge, lang, schedule]);

  useEffect(() => {
    alive.current = true;
    void poll();
    return () => {
      alive.current = false;
      if (timer.current) clearTimeout(timer.current);
    };
  }, [poll]);

  const act = async (run: () => Promise<EnsureResult>, done: string | null): Promise<void> => {
    setBusy(true);
    setProblem(null);
    setNote(null);
    try {
      const result = await run();
      if (!alive.current) return;
      if (result.ok) {
        accept(result);
        setNote(done);
        schedule(result.view, () => void poll());
      } else setProblem(describeFailure(result, lang, 'action'));
    } catch {
      if (alive.current) setProblem(describeFailure({ ok: false, code: 'unavailable' }, lang, 'action'));
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const toApprove = view ? stepsNeedingOk(view) : [];
  const install = (): Promise<void> => act(() => bridge!.ensure.start(), pick(STRINGS.started, lang));
  const stop = (): Promise<void> => act(() => bridge!.ensure.cancel(), pick(STRINGS.stopped, lang));
  const approve = async (): Promise<void> => {
    setConfirming(false);
    await act(() => bridge!.ensure.confirm(toApprove.map((s) => s.id)), pick(STRINGS.started, lang));
  };

  return (
    <Box component="main" sx={{ p: 3 }}>
      <Stack spacing={2}>
        <Typography component="h1" variant="h5">
          {pick(STRINGS.title, lang)}
        </Typography>
        {view === null ? (
          problem ? (
            <ProblemAlert problem={problem} lang={lang} />
          ) : (
            <Typography role="status">{pick(STRINGS.loading, lang)}</Typography>
          )
        ) : (
          <>
            <Alert severity={stateSeverity(view.state)} icon={<span aria-hidden="true">{STATE_MARK[view.state]}</span>} data-testid="state" role="status">
              <Typography component="div" sx={{ fontWeight: 700 }}>
                <Box component="span" sx={HIDDEN}>{`${stateText(view.state, lang)}: `}</Box>
                {view.message}
              </Typography>
            </Alert>
            {view.state === 'installing' && view.progress.total > 0 ? (
              <Box>
                <LinearProgress variant="determinate" value={Math.round((view.progress.done / view.progress.total) * 100)} aria-label={progressText(view, lang)} data-testid="progress" />
                <Typography variant="body2" sx={{ mt: 0.5 }}>
                  {progressText(view, lang)}
                </Typography>
              </Box>
            ) : null}
            {view.nextAction.kind !== 'none' && view.nextAction.kind !== 'wait' && view.nextAction.text !== '' ? (
              <Typography data-testid="next">
                <strong>{pick(STRINGS.next, lang)}</strong>
                <br />
                {view.nextAction.text}
              </Typography>
            ) : null}
            <Stack direction="row" spacing={1} sx={{ flexWrap: 'wrap' }}>
              {view.state === 'installing' ? (
                <Button variant="outlined" disabled={busy} onClick={() => void stop()} data-testid="stop">
                  {pick(STRINGS.stop, lang)}
                </Button>
              ) : canInstall(view) ? (
                <Button variant="contained" disabled={busy} onClick={() => void install()} data-testid="install">
                  {pick(busy ? STRINGS.working : STRINGS.install, lang)}
                </Button>
              ) : null}
              {toApprove.length > 0 && view.state !== 'installing' ? (
                <Button variant={canInstall(view) ? 'outlined' : 'contained'} disabled={busy} onClick={() => setConfirming(true)} data-testid="approve">
                  {pick(STRINGS.approve, lang)}
                </Button>
              ) : null}
            </Stack>
            {note ? (
              <Alert severity="info" data-testid="note">
                {note}
              </Alert>
            ) : null}
            {problem ? <ProblemAlert problem={problem} lang={lang} /> : null}
            {view.steps.length > 0 ? (
              <Box component="section" aria-labelledby="parts">
                <Typography id="parts" component="h2" variant="subtitle1" sx={{ fontWeight: 700 }}>
                  {pick(STRINGS.parts, lang)}
                </Typography>
                <List dense data-testid="steps">
                  {view.steps.map((s) => (
                    <ListItem key={s.id} divider data-testid={`step-${s.id}`} data-status={s.status}>
                      <ListItemText
                        primary={`${s.title} · ${statusText(s.status, lang)}`}
                        secondary={
                          <>
                            {s.detail}
                            {s.status === 'needs_confirm' ? <Box component="span" sx={{ display: 'block' }}>{warningFor(s.id, lang)}</Box> : null}
                            {s.code ? <Box component="span" sx={{ display: 'block' }}>{s.code}</Box> : null}
                          </>
                        }
                        slotProps={{ secondary: { component: 'div' } }}
                      />
                    </ListItem>
                  ))}
                </List>
              </Box>
            ) : null}
            {view.state !== 'complete' ? <Typography variant="caption">{pick(STRINGS.typed, lang)}</Typography> : null}
          </>
        )}
        <Typography variant="body2" color="text.secondary">
          {pick(STRINGS.intro, lang)}
        </Typography>
      </Stack>
      <Dialog open={confirming} onClose={() => setConfirming(false)} aria-labelledby="ensure-confirm-title" data-testid="confirm">
        <DialogTitle id="ensure-confirm-title">{pick(STRINGS.confirmTitle, lang)}</DialogTitle>
        <DialogContent>
          <Typography sx={{ mb: 1 }}>{pick(STRINGS.confirmBody, lang)}</Typography>
          <List dense>
            {toApprove.map((s) => (
              <ListItem key={s.id}>
                <ListItemText primary={s.title} secondary={warningFor(s.id, lang)} />
              </ListItem>
            ))}
          </List>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirming(false)} data-testid="confirm-no">
            {pick(STRINGS.confirmNo, lang)}
          </Button>
          <Button variant="contained" onClick={() => void approve()} data-testid="confirm-yes" autoFocus>
            {pick(STRINGS.confirmYes, lang)}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
