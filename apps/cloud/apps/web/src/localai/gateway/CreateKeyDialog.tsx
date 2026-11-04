// The Create key dialog and the show-once panel (docs/specs/gateway.md section 3.8). Step 1: name, models (empty = all)
// and expiry. Step 2, after the API answers: the key itself, shown ONCE, with Copy and the ready snippets.
//
// THE TOKEN. It lives in this component's state and nowhere else: not in the parent, not in storage, not in a URL. The
// parent mounts the dialog only while it is open, so closing it unmounts the component and drops the token. While the
// token is on screen, Escape and a click on the backdrop do not close the dialog (a stray click must not lose a key that
// can never be shown again); the button "I have stored it" does.
import { Alert, Autocomplete, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Radio, RadioGroup, TextField, Typography } from '@mui/material';
import { useEffect, useId, useState } from 'react';

import { CodeBlock } from '../wizard/CodeBlock';
import { CopyButton } from '../wizard/CopyButton';
import { KeySnippets } from './KeySnippets';
import type { CreatedKey, CreateKeyInput, KeyDefaults } from './types';

export interface CreateKeyDialogProps {
  /** gateway:admin: may create a service key. */
  canCreateService: boolean;
  defaults: KeyDefaults;
  createKey: (input: CreateKeyInput) => Promise<CreatedKey>;
  /** Model ids to suggest. Failing to load them only loses the suggestions. */
  loadSuggestions?: () => Promise<string[]>;
  /** Called with the new key's row (never the token) so the list can refresh. */
  onCreated: () => void;
  onClose: () => void;
  /** The page's origin, for the snippets (tests). */
  origin?: string;
}

export function CreateKeyDialog({ canCreateService, defaults, createKey, loadSuggestions, onCreated, onClose, origin = window.location.origin }: CreateKeyDialogProps) {
  const titleId = useId();
  const [name, setName] = useState('');
  const [models, setModels] = useState<string[]>([]);
  const [kind, setKind] = useState<'user' | 'service'>('user');
  const [days, setDays] = useState(String(defaults.expiresInDays));
  const [suggestions, setSuggestions] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The one place the token is held.
  const [created, setCreated] = useState<CreatedKey | null>(null);

  useEffect(() => {
    let live = true;
    loadSuggestions?.().then(
      (ids) => live && setSuggestions(ids),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [loadSuggestions]);

  const service = kind === 'service';
  const daysNumber = Number(days);
  const daysValid = service ? days === '' || (Number.isInteger(daysNumber) && daysNumber >= 1 && daysNumber <= 3650) : Number.isInteger(daysNumber) && daysNumber >= 1 && daysNumber <= defaults.maxUserKeyDays;
  const nameValid = name.trim().length >= 1 && name.trim().length <= 80;

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const result = await createKey({
        name: name.trim(),
        ...(models.length > 0 ? { allowedModels: models } : {}),
        ...(service ? { kind: 'service' as const, expiresInDays: days === '' ? null : daysNumber } : { expiresInDays: daysNumber }),
      });
      setCreated(result);
      onCreated();
    } catch (caught) {
      setError(caught instanceof Error && caught.message ? caught.message : 'Could not create the key.');
    } finally {
      setBusy(false);
    }
  };

  const showing = created !== null;
  return (
    <Dialog
      open
      onClose={(_event, reason) => {
        // The key can never be shown again: only the button closes the dialog while it is on screen.
        if (showing && (reason === 'backdropClick' || reason === 'escapeKeyDown')) return;
        if (!busy) onClose();
      }}
      aria-labelledby={titleId}
      fullWidth
      maxWidth="sm"
    >
      <DialogTitle id={titleId}>{showing ? 'Your new key' : 'Create key'}</DialogTitle>
      {showing ? (
        <>
          <DialogContent>
            <Alert severity="warning" sx={{ mb: 2 }}>
              Store it now; it can't be shown again.
            </Alert>
            <Typography variant="body2" sx={{ mb: 0.5 }}>
              {created.key.name}
            </Typography>
            <Box sx={{ position: 'relative' }}>
              <CodeBlock label="The new key">{created.token}</CodeBlock>
            </Box>
            <Box sx={{ mt: 1, mb: 2 }}>
              <CopyButton text={created.token} context="the key" />
            </Box>
            <KeySnippets origin={origin} models={created.key.allowedModels} />
          </DialogContent>
          <DialogActions>
            <Button variant="contained" onClick={onClose}>
              I have stored it
            </Button>
          </DialogActions>
        </>
      ) : (
        <>
          <DialogContent sx={{ display: 'flex', flexDirection: 'column', gap: 2, pt: 1 }}>
            {error ? <Alert severity="error">{error}</Alert> : null}
            <TextField
              label="Name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="OpenCode laptop"
              helperText="Something you will recognise, one key per program or computer."
              required
              autoFocus
              slotProps={{ htmlInput: { maxLength: 80 } }}
            />
            <Autocomplete
              multiple
              freeSolo
              options={suggestions}
              value={models}
              onChange={(_event, value) => setModels((value as unknown[]).map((entry) => String(entry).trim()).filter((entry) => entry !== ''))}
              renderInput={(params) => <TextField {...params} label="Models" helperText="Leave empty to allow every model. Type an id and press Enter to add it." />}
            />
            {canCreateService ? (
              <RadioGroup row aria-label="Key type" value={kind} onChange={(event) => setKind(event.target.value as 'user' | 'service')}>
                <FormControlLabel value="user" control={<Radio />} label="My key" />
                <FormControlLabel value="service" control={<Radio />} label="Service key (a program such as Open WebUI)" />
              </RadioGroup>
            ) : null}
            <TextField
              label="Expires in (days)"
              type="number"
              value={days}
              onChange={(event) => setDays(event.target.value)}
              error={!daysValid}
              helperText={
                service ? 'Leave empty for a key that never expires.' : `From 1 to ${defaults.maxUserKeyDays} days. Default ${defaults.expiresInDays}.`
              }
              slotProps={{ htmlInput: { min: 1, max: service ? 3650 : defaults.maxUserKeyDays, step: 1 } }}
            />
          </DialogContent>
          <DialogActions>
            <Button onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="contained" onClick={() => void submit()} disabled={busy || !nameValid || !daysValid}>
              Create
            </Button>
          </DialogActions>
        </>
      )}
    </Dialog>
  );
}
