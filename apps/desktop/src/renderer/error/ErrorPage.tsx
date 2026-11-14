// The error page (docs/specs/desktop.md 4.2 and 5): the doctor code, the text in both languages (the chosen one first),
// the buttons that fix the problem, and the typed command folded under "Use a terminal instead". The actions go through
// the shell bridge; the page only names an action, the main process picks the command. The token never reaches it.
import { Alert, Box, Button, Chip, Container, Divider, Paper, Stack, Typography } from '@mui/material';
import { type ReactElement, type ReactNode, useState } from 'react';
import {
  ACTION_LABEL,
  PAGE_STRINGS,
  START_FAILURE_TEXT,
  type Bilingual,
  type ErrorAction,
  type Lang,
  errorText,
  isDskCode,
  isStartFailureReason,
  pick,
} from '../../main/i18n';
import type { ActionResult } from '../../shared/types';
import { ALWAYS_USABLE, SECONDARY_ACTIONS, errorPageLayout, fixFailureNote } from './model';

export interface ErrorPageProps {
  code: string | null;
  lang: Lang;
  /** `systemctl is-active` answer shown on the "agent is not running" page. */
  unit?: string | null;
  /** The shell bridge; absent in a plain browser (the buttons then do nothing). */
  act?: (action: ErrorAction, code: string) => Promise<ActionResult>;
}

type NoteSeverity = 'info' | 'success' | 'warning' | 'error';
interface Note {
  severity: NoteSeverity;
  text: string;
}

const other = (lang: Lang): Lang => (lang === 'en' ? 'es' : 'en');

function LanguageBlock({ code, lang, primary }: { code: string | null; lang: Lang; primary: boolean }): ReactElement {
  const text = isDskCode(code) ? errorText(code, lang) : null;
  return (
    <Box lang={lang} data-testid={`text-${lang}`} sx={{ opacity: primary ? 1 : 0.85 }}>
      <Typography component={primary ? 'h1' : 'h2'} variant={primary ? 'h4' : 'h6'} gutterBottom>
        {text ? text.title : pick(PAGE_STRINGS.unknownTitle, lang)}
      </Typography>
      <Typography variant={primary ? 'body1' : 'body2'} sx={{ mb: 1.5 }}>
        {text ? text.message : pick(PAGE_STRINGS.unknownFix, lang)}
      </Typography>
      {text ? (
        <Typography variant={primary ? 'body1' : 'body2'}>
          <strong>{pick(PAGE_STRINGS.whatToDo, lang)}:</strong> {text.fix}
        </Typography>
      ) : null}
    </Box>
  );
}

const PRE_SX = { m: 0, p: 1.5, bgcolor: 'action.hover', borderRadius: 1, overflowX: 'auto', fontFamily: 'ui-monospace, monospace' } as const;

/** A native `<details>` disclosure: closed by default, opened with Enter or Space on its summary. */
function Disclosure({ summary, testId, children }: { summary: string; testId: string; children: ReactNode }): ReactElement {
  return (
    <Box
      component="details"
      data-testid={testId}
      sx={{
        '& > summary': { cursor: 'pointer', fontWeight: 600, py: 0.5, borderRadius: 1 },
        '& > summary:focus-visible': { outline: '2px solid', outlineColor: 'primary.main', outlineOffset: 2 },
      }}
    >
      <summary>{summary}</summary>
      <Stack spacing={1.5} sx={{ mt: 1 }}>
        {children}
      </Stack>
    </Box>
  );
}

/** What a failed "Start agent" shows (LAI-DSK-005): plain words first, the raw status folded under "Details". */
function StartFailure({ lang, excerpt, reason, onCopy }: { lang: Lang; excerpt: string | null; reason: string | null; onCopy: () => void }): ReactElement {
  const text = errorText('LAI-DSK-005', lang);
  const second = errorText('LAI-DSK-005', other(lang));
  const why = isStartFailureReason(reason) ? START_FAILURE_TEXT[reason] : null;
  return (
    <Alert severity="error" data-testid="start-failure" sx={{ '& .MuiAlert-message': { width: '100%' } }}>
      <Stack spacing={1.5}>
        <Chip size="small" color="error" label={`${pick(PAGE_STRINGS.code, lang)}: LAI-DSK-005`} data-testid="failure-code" sx={{ alignSelf: 'flex-start' }} />
        <Typography variant="subtitle1" component="p" sx={{ fontWeight: 600 }}>
          {text.title}
        </Typography>
        {why ? (
          <Typography variant="body2" data-testid="failure-what">
            {pick(why.what, lang)} <strong>{pick(PAGE_STRINGS.whatToDo, lang)}:</strong> {pick(why.next, lang)}
          </Typography>
        ) : (
          <Typography variant="body2" data-testid="failure-what">
            {text.message} <strong>{pick(PAGE_STRINGS.whatToDo, lang)}:</strong> {text.fix}
          </Typography>
        )}
        <Typography variant="body2" lang={other(lang)} sx={{ opacity: 0.85 }}>
          <strong>{second.title}.</strong> {why ? `${pick(why.what, other(lang))} ${pick(why.next, other(lang))}` : `${second.message} ${second.fix}`}
        </Typography>
        <Disclosure summary={pick(PAGE_STRINGS.detailsSummary, lang)} testId="failure-details">
          <Typography variant="caption">{pick(PAGE_STRINGS.statusExcerpt, lang)}</Typography>
          <Box
            component="pre"
            data-testid="status-excerpt"
            sx={{ ...PRE_SX, overflow: 'auto', maxHeight: 240, fontSize: '0.8rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}
          >
            {excerpt ?? pick(PAGE_STRINGS.noStatusExcerpt, lang)}
          </Box>
          <Typography variant="body2">{pick(PAGE_STRINGS.terminalHint, lang)}</Typography>
          <Box component="pre" data-testid="failure-command" sx={PRE_SX}>
            {text.command}
          </Box>
          <Box>
            <Button size="small" variant="outlined" onClick={onCopy}>
              {pick(PAGE_STRINGS.copyCommand, lang)}
            </Button>
          </Box>
        </Disclosure>
      </Stack>
    </Alert>
  );
}

export function ErrorPage({ code, lang, unit, act }: ErrorPageProps): ReactElement {
  const [note, setNote] = useState<Note | null>(null);
  /** The button whose work is running (one at a time); its label then says it is working. */
  const [busy, setBusy] = useState<ErrorAction | null>(null);
  const [failure, setFailure] = useState<{ excerpt: string | null; reason: string | null } | null>(null);
  /** "Allow this user" worked: the page now asks to log out and in. */
  const [allowed, setAllowed] = useState(false);
  const text = isDskCode(code) ? errorText(code, lang) : null;
  const layout = errorPageLayout(code);
  const say = (severity: NoteSeverity, message: Bilingual): void => setNote({ severity, text: pick(message, lang) });

  const copy = async (forCode: string): Promise<void> => {
    try {
      const result = act ? await act('copy-command', forCode) : { ok: false };
      if (result.ok) say('info', PAGE_STRINGS.copied);
      else say('warning', PAGE_STRINGS.actionFailed);
    } catch {
      say('warning', PAGE_STRINGS.actionFailed);
    }
  };

  const run = async (action: ErrorAction): Promise<ActionResult | null> => {
    if (!act || !code) return null;
    try {
      return await act(action, code);
    } catch {
      return { ok: false };
    }
  };

  const press = async (action: ErrorAction): Promise<void> => {
    if (!act || !code) return;
    if (action === 'copy-command') return copy(code);
    // One privileged call at a time: the buttons are disabled while one runs, and the main process ignores a second too.
    if (busy !== null) return;
    setNote(null);
    switch (action) {
      case 'start-agent': {
        setBusy(action);
        setFailure(null);
        const result = await run(action);
        // Success: the main process restarts the start-up sequence and replaces this page; keep showing "Starting...".
        if (result && (result.ok || result.busy)) return;
        setFailure({ excerpt: result?.statusExcerpt ?? null, reason: result?.reason ?? null });
        setBusy(null);
        return;
      }
      case 'allow-user': {
        setBusy(action);
        const result = await run(action);
        setBusy(null);
        if (result?.ok) {
          setAllowed(true);
          return;
        }
        const message = result ? fixFailureNote(result) : PAGE_STRINGS.fixFailed;
        if (message) say(result?.reason === 'cancelled' ? 'warning' : 'error', message);
        return;
      }
      case 'repair-access': {
        setBusy(action);
        const result = await run(action);
        if (result?.ok) {
          // The main process runs the start-up sequence again, which replaces this page.
          say('success', PAGE_STRINGS.repairDone);
          return;
        }
        setBusy(null);
        const message = result ? fixFailureNote(result) : PAGE_STRINGS.fixFailed;
        if (message) say(result?.reason === 'cancelled' ? 'warning' : 'error', message);
        return;
      }
      case 'rotate-token': {
        setBusy(action);
        const result = await run(action);
        if (result?.ok) {
          // The main process waits for the agent to accept the new token, then runs the start-up sequence again.
          say('success', PAGE_STRINGS.rotateDone);
          return;
        }
        setBusy(null);
        const message = result ? fixFailureNote(result) : PAGE_STRINGS.fixFailed;
        if (message) say(result?.reason === 'cancelled' ? 'warning' : 'error', message);
        return;
      }
      case 'restart-computer': {
        setBusy(action);
        const result = await run(action);
        setBusy(null);
        // Cancel in the confirmation: nothing to say. Success: the computer is going down.
        if (result && !result.ok && !result.cancelled && !result.busy) say('error', PAGE_STRINGS.rebootFailed);
        return;
      }
      case 'open-library': {
        const result = await run(action);
        if (result?.ok) say('success', PAGE_STRINGS.libraryOpened);
        else say('warning', result?.reason === 'library-down' ? PAGE_STRINGS.libraryDown : PAGE_STRINGS.actionFailed);
        return;
      }
      case 'logout': {
        const result = await run(action);
        if (!result?.ok) say('info', PAGE_STRINGS.logoutHint);
        return;
      }
      default: {
        const result = await run(action);
        if (!result?.ok && action !== 'open-portal') say('warning', PAGE_STRINGS.actionFailed);
      }
    }
  };

  const label = (action: ErrorAction): string => {
    if (busy === action) return pick(action === 'start-agent' ? PAGE_STRINGS.starting : PAGE_STRINGS.working, lang);
    return pick(ACTION_LABEL[action], lang);
  };

  return (
    <Container component="main" maxWidth="md" sx={{ py: 4 }}>
      <Paper variant="outlined" sx={{ p: { xs: 2, sm: 4 } }}>
        <Stack spacing={3}>
          <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
            <Chip label={`${pick(PAGE_STRINGS.code, lang)}: ${code ?? '-'}`} color={text?.severity === 'warning' ? 'warning' : 'error'} data-testid="code" />
            {unit ? <Chip variant="outlined" label={`${pick(PAGE_STRINGS.unitState, lang)}: ${unit}`} data-testid="unit" /> : null}
          </Stack>
          <LanguageBlock code={code} lang={lang} primary />
          {layout.leadCommand !== null ? (
            <Box component="pre" data-testid="command" sx={{ ...PRE_SX, p: 2 }}>
              {layout.leadCommand}
            </Box>
          ) : null}
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }} data-testid="actions">
            {layout.buttons.map((action) => (
              <Button
                key={action}
                data-action={action}
                variant={SECONDARY_ACTIONS.includes(action) ? 'outlined' : 'contained'}
                disabled={busy !== null && !ALWAYS_USABLE.includes(action)}
                onClick={() => void press(action)}
              >
                {label(action)}
              </Button>
            ))}
          </Stack>
          {busy !== null && busy !== 'restart-computer' ? (
            <Typography variant="body2" role="status" data-testid={busy === 'start-agent' ? 'starting-hint' : 'password-hint'}>
              {pick(busy === 'start-agent' ? PAGE_STRINGS.startingHint : PAGE_STRINGS.passwordHint, lang)}
            </Typography>
          ) : null}
          {allowed ? (
            <Alert severity="success" data-testid="allow-done" role="status" sx={{ '& .MuiAlert-message': { width: '100%' } }}>
              <Stack spacing={1.5}>
                <Typography variant="body1">{pick(PAGE_STRINGS.allowUserDone, lang)}</Typography>
                <Typography variant="body2" lang={other(lang)} sx={{ opacity: 0.85 }}>
                  {pick(PAGE_STRINGS.allowUserDone, other(lang))}
                </Typography>
                <Box>
                  <Button variant="contained" autoFocus data-action="logout" onClick={() => void press('logout')}>
                    {pick(PAGE_STRINGS.logout, lang)}
                  </Button>
                </Box>
              </Stack>
            </Alert>
          ) : null}
          {failure ? <StartFailure lang={lang} excerpt={failure.excerpt} reason={failure.reason} onCopy={() => void copy('LAI-DSK-005')} /> : null}
          {layout.buttons.includes('open-browser') ? <Typography variant="body2">{pick(PAGE_STRINGS.browserHint, lang)}</Typography> : null}
          {note ? (
            <Alert severity={note.severity} role="status" data-testid="note">
              {note.text}
            </Alert>
          ) : null}
          {layout.terminalCommand !== null ? (
            <Disclosure summary={pick(PAGE_STRINGS.terminalSummary, lang)} testId="terminal">
              <Typography variant="body2">{pick(PAGE_STRINGS.terminalHint, lang)}</Typography>
              <Box component="pre" data-testid="command" sx={{ ...PRE_SX, p: 2 }}>
                {layout.terminalCommand}
              </Box>
              <Box>
                <Button size="small" variant="outlined" data-action="copy-command" onClick={() => void press('copy-command')}>
                  {pick(PAGE_STRINGS.copyCommand, lang)}
                </Button>
              </Box>
            </Disclosure>
          ) : null}
          <Divider />
          <LanguageBlock code={code} lang={other(lang)} primary={false} />
        </Stack>
      </Paper>
    </Container>
  );
}
