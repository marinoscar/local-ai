// "Import release…": a `catalog-YYYY.MM.DD-N` tag, queued as a server job (`POST /api/localai/catalog/import`, 202).
// The draft appears in the table when the download finishes. A version the cloud already has is a 409.
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, TextField, Typography } from '@mui/material';
import { useState, type FormEvent } from 'react';

import { errorMessage, errorStatus, type CatalogApi } from '../api/catalog';
import { versionOfTag } from './catalogRules';

export interface ImportDialogProps {
  client: CatalogApi;
  open: boolean;
  onClose(): void;
  onQueued(tag: string, version: string): void;
}

export function ImportDialog({ client, open, onClose, onQueued }: ImportDialogProps) {
  return open ? <ImportDialogBody client={client} onClose={onClose} onQueued={onQueued} /> : null;
}

function ImportDialogBody({ client, onClose, onQueued }: Omit<ImportDialogProps, 'open'>) {
  const [tag, setTag] = useState('');
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const trimmed = tag.trim();
  const version = versionOfTag(trimmed);
  const invalid = touched && version === null;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (version === null) return;
    setBusy(true);
    setError(null);
    try {
      const result = await client.importRelease(trimmed);
      onQueued(trimmed, result.version);
    } catch (caught) {
      setError(errorStatus(caught) === 409 ? 'This version already exists.' : errorMessage(caught, 'Could not start the import. Try again.'));
      setBusy(false);
    }
  };

  return (
    <Dialog open onClose={busy ? undefined : onClose} aria-labelledby="import-dialog-title" fullWidth maxWidth="xs">
      <Box component="form" onSubmit={(event) => void submit(event)} noValidate>
        <DialogTitle id="import-dialog-title">Import a catalog release</DialogTitle>
        <DialogContent>
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.5, pt: 0.5 }}>
            <Typography variant="body2" color="text.secondary">
              Downloads the release from GitHub and saves it as a draft. The catalog checker runs on it by itself.
            </Typography>
            <TextField
              label="Release tag"
              value={tag}
              onChange={(event) => setTag(event.target.value)}
              onBlur={() => setTouched(true)}
              placeholder="catalog-2026.11.02-1"
              error={invalid}
              helperText={invalid ? 'Use catalog-YYYY.MM.DD-N, for example catalog-2026.11.02-1.' : 'catalog-YYYY.MM.DD-N'}
              autoFocus
              autoComplete="off"
              disabled={busy}
              slotProps={{ htmlInput: { spellCheck: false, autoCapitalize: 'off' } }}
            />
            {error ? <Alert severity="error">{error}</Alert> : null}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="contained" disabled={busy || (touched && version === null)}>
            {busy ? 'Importing…' : 'Import'}
          </Button>
        </DialogActions>
      </Box>
    </Dialog>
  );
}
