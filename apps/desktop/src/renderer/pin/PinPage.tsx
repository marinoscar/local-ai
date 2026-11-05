// The PIN window (docs/specs/desktop.md 4.6): digits only, 4 to 8, masked, English or Spanish. It talks to the main
// process through `window.localaiPin` (src/preload/pin.ts) and keeps the PIN in React state for as long as it is typed:
// the field is emptied after every answer and when the window goes away.
import { Alert, Box, Button, Stack, TextField, Typography } from '@mui/material';
import { type FormEvent, type ReactElement, useEffect, useRef, useState } from 'react';
import type { LocalaiPin } from '../../shared/types';
import { type Feedback, STRINGS, canSubmit, feedbackFor, parseQuery, pick, sanitizePin } from './model';

export interface PinPageProps {
  bridge?: LocalaiPin;
  /** The page's `location.search`. */
  search: string;
}

export function PinPage({ bridge, search }: PinPageProps): ReactElement {
  const { purpose, lang } = parseQuery(search);
  const [pin, setPin] = useState('');
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const field = useRef<HTMLInputElement | null>(null);

  // The PIN does not outlive the window.
  useEffect(() => {
    const wipe = (): void => setPin('');
    window.addEventListener('pagehide', wipe);
    return () => {
      window.removeEventListener('pagehide', wipe);
      wipe();
    };
  }, []);

  useEffect(() => {
    if (!busy && !feedback?.final) field.current?.focus();
  }, [busy, feedback]);

  const cancel = (): void => bridge?.cancel();

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    if (!bridge || busy || !canSubmit(pin)) return;
    setBusy(true);
    const typed = pin;
    setPin('');
    try {
      const result = await bridge.submit(typed);
      // On success the main process closes this window; until then nothing more is shown.
      setFeedback(feedbackFor(result, lang));
    } catch {
      setFeedback(feedbackFor({ ok: false, code: 'unavailable' }, lang));
    } finally {
      setBusy(false);
    }
  };

  const final = feedback?.final === true;
  return (
    <Box component="main" onKeyDown={(e) => e.key === 'Escape' && cancel()} sx={{ p: 3 }}>
      <Stack component="form" spacing={2} onSubmit={(e) => void submit(e)} noValidate>
        <Typography component="h1" variant="h6">
          {pick(purpose === 'rotate-key' ? STRINGS.titleRotateKey : STRINGS.titleLeaveSurvival, lang)}
        </Typography>
        <Typography variant="body2">{pick(purpose === 'rotate-key' ? STRINGS.promptRotateKey : STRINGS.promptLeaveSurvival, lang)}</Typography>
        <TextField
          label={pick(STRINGS.label, lang)}
          type="password"
          value={pin}
          disabled={busy || final}
          onChange={(e) => setPin(sanitizePin(e.target.value))}
          inputRef={field}
          autoFocus
          slotProps={{ htmlInput: { inputMode: 'numeric', autoComplete: 'off', maxLength: 8, 'data-testid': 'pin-input' } }}
        />
        {feedback ? (
          <Alert severity={final ? 'error' : 'warning'} role="alert" data-testid="pin-feedback">
            <strong>{feedback.text}</strong>
            <br />
            {pick(STRINGS.fix, lang)}: {feedback.fix}
            {feedback.code ? <Typography component="div" variant="caption" data-testid="pin-code">{feedback.code}</Typography> : null}
          </Alert>
        ) : null}
        <Stack direction="row" spacing={1} sx={{ justifyContent: 'flex-end' }}>
          <Button onClick={cancel} data-testid="pin-cancel">
            {pick(final ? STRINGS.close : STRINGS.cancel, lang)}
          </Button>
          <Button type="submit" variant="contained" disabled={busy || final || !canSubmit(pin)} data-testid="pin-ok">
            {pick(busy ? STRINGS.checking : STRINGS.ok, lang)}
          </Button>
        </Stack>
      </Stack>
    </Box>
  );
}
