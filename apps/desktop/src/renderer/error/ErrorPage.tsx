// The error page (docs/specs/desktop.md 4.2 and 5): the doctor code, the text in both languages (the chosen one first),
// the exact fix and the buttons. The actions go through the shell bridge; the token never reaches this page.
import { Alert, Box, Button, Chip, Container, Divider, Paper, Stack, Typography } from '@mui/material';
import { type ReactElement, useState } from 'react';
import {
  ACTION_LABEL,
  PAGE_STRINGS,
  type ErrorAction,
  type Lang,
  errorText,
  isDskCode,
  pick,
} from '../../main/i18n';

export interface ErrorPageProps {
  code: string | null;
  lang: Lang;
  /** `systemctl is-active` answer shown on the "agent is not running" page. */
  unit?: string | null;
  /** The shell bridge; absent in a plain browser (the buttons then do nothing). */
  act?: (action: ErrorAction, code: string) => Promise<{ ok: boolean; code?: string; statusExcerpt?: string; busy?: boolean }>;
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

/** What a failed "Start agent" shows under the buttons (LAI-DSK-005): both languages, the status excerpt, the command. */
function StartFailure({ lang, excerpt, onCopy }: { lang: Lang; excerpt: string | null; onCopy: () => void }): ReactElement {
  const text = errorText('LAI-DSK-005', lang);
  const second = errorText('LAI-DSK-005', other(lang));
  return (
    <Alert severity="error" data-testid="start-failure" sx={{ '& .MuiAlert-message': { width: '100%' } }}>
      <Stack spacing={1.5}>
        <Chip size="small" color="error" label={`${pick(PAGE_STRINGS.code, lang)}: LAI-DSK-005`} data-testid="failure-code" sx={{ alignSelf: 'flex-start' }} />
        <Typography variant="subtitle1" component="p" sx={{ fontWeight: 600 }}>
          {text.title}
        </Typography>
        <Typography variant="body2">
          {text.message} <strong>{pick(PAGE_STRINGS.whatToDo, lang)}:</strong> {text.fix}
        </Typography>
        <Box component="pre" data-testid="failure-command" sx={{ m: 0, p: 1.5, bgcolor: 'action.hover', borderRadius: 1, overflowX: 'auto', fontFamily: 'ui-monospace, monospace' }}>
          {text.command}
        </Box>
        <Typography variant="caption">{pick(PAGE_STRINGS.statusExcerpt, lang)}</Typography>
        <Box
          component="pre"
          data-testid="status-excerpt"
          sx={{ m: 0, p: 1.5, bgcolor: 'action.hover', borderRadius: 1, overflow: 'auto', maxHeight: 240, fontFamily: 'ui-monospace, monospace', fontSize: '0.8rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}
        >
          {excerpt ?? pick(PAGE_STRINGS.noStatusExcerpt, lang)}
        </Box>
        <Typography variant="body2" lang={other(lang)} sx={{ opacity: 0.85 }}>
          <strong>{second.title}.</strong> {second.message} {second.fix}
        </Typography>
        <Box>
          <Button size="small" variant="outlined" onClick={onCopy}>
            {pick(PAGE_STRINGS.copyCommand, lang)}
          </Button>
        </Box>
      </Stack>
    </Alert>
  );
}

export function ErrorPage({ code, lang, unit, act }: ErrorPageProps): ReactElement {
  const [note, setNote] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [failure, setFailure] = useState<{ excerpt: string | null } | null>(null);
  const text = isDskCode(code) ? errorText(code, lang) : null;
  const actions: readonly ErrorAction[] = text ? text.actions : ['open-portal'];

  // LAI-DSK-005 has its own "Copy command"; the main process copies the catalog's command for that code.
  const copyFailureCommand = async (): Promise<void> => {
    try {
      const result = act ? await act('copy-command', 'LAI-DSK-005') : { ok: false };
      setNote(result.ok ? pick(PAGE_STRINGS.copied, lang) : pick(PAGE_STRINGS.actionFailed, lang));
    } catch {
      setNote(pick(PAGE_STRINGS.actionFailed, lang));
    }
  };

  const press = async (action: ErrorAction): Promise<void> => {
    if (!act || !code) return;
    if (action === 'start-agent') {
      // One start at a time: the button is disabled while it runs, and the main process ignores a second request too.
      if (starting) return;
      setStarting(true);
      setFailure(null);
      setNote(null);
      try {
        const result = await act(action, code);
        // Success: the main process restarts the start-up sequence and replaces this page; keep showing "Starting...".
        if (result.ok || result.busy) return;
        setFailure({ excerpt: result.statusExcerpt ?? null });
      } catch {
        setFailure({ excerpt: null });
      }
      setStarting(false);
      return;
    }
    try {
      const result = await act(action, code);
      if (action === 'copy-command' && result.ok) setNote(pick(PAGE_STRINGS.copied, lang));
      else if (!result.ok && action !== 'open-portal') setNote(action === 'logout' ? pick(PAGE_STRINGS.logoutHint, lang) : pick(PAGE_STRINGS.actionFailed, lang));
      else setNote(null);
    } catch {
      setNote(pick(PAGE_STRINGS.actionFailed, lang));
    }
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
          {text?.command ? (
            <Box component="pre" data-testid="command" sx={{ m: 0, p: 2, bgcolor: 'action.hover', borderRadius: 1, overflowX: 'auto', fontFamily: 'ui-monospace, monospace' }}>
              {text.command}
            </Box>
          ) : null}
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
            {actions.map((action) => (
              <Button
                key={action}
                variant={action === 'open-portal' ? 'outlined' : 'contained'}
                disabled={action === 'start-agent' && starting}
                onClick={() => void press(action)}
              >
                {action === 'start-agent' && starting ? pick(PAGE_STRINGS.starting, lang) : pick(ACTION_LABEL[action], lang)}
              </Button>
            ))}
          </Stack>
          {starting ? (
            <Typography variant="body2" role="status" data-testid="starting-hint">
              {pick(PAGE_STRINGS.startingHint, lang)}
            </Typography>
          ) : null}
          {failure ? <StartFailure lang={lang} excerpt={failure.excerpt} onCopy={() => void copyFailureCommand()} /> : null}
          {actions.includes('open-browser') ? <Typography variant="body2">{pick(PAGE_STRINGS.browserHint, lang)}</Typography> : null}
          {note ? <Alert severity="info">{note}</Alert> : null}
          <Divider />
          <LanguageBlock code={code} lang={other(lang)} primary={false} />
        </Stack>
      </Paper>
    </Container>
  );
}
