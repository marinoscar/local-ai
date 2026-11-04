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
  act?: (action: ErrorAction, code: string) => Promise<{ ok: boolean; code?: string }>;
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

export function ErrorPage({ code, lang, unit, act }: ErrorPageProps): ReactElement {
  const [note, setNote] = useState<string | null>(null);
  const text = isDskCode(code) ? errorText(code, lang) : null;
  const actions: readonly ErrorAction[] = text ? text.actions : ['open-portal'];

  const press = async (action: ErrorAction): Promise<void> => {
    if (!act || !code) return;
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
              <Button key={action} variant={action === 'open-portal' ? 'outlined' : 'contained'} onClick={() => void press(action)}>
                {pick(ACTION_LABEL[action], lang)}
              </Button>
            ))}
          </Stack>
          {actions.includes('open-browser') ? <Typography variant="body2">{pick(PAGE_STRINGS.browserHint, lang)}</Typography> : null}
          {note ? <Alert severity="info">{note}</Alert> : null}
          <Divider />
          <LanguageBlock code={code} lang={other(lang)} primary={false} />
        </Stack>
      </Paper>
    </Container>
  );
}
