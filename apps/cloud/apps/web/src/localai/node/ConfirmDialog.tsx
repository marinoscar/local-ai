// A confirmation before an action that changes a node (docs/specs/cloud-app.md section 3.7, "UI confirmation"). It
// states what will happen, runs `onConfirm` and stays open with the refusal if the action fails, so the operator reads
// the reason. `TypedConfirmDialog` is the same dialog with a text the operator must type exactly.
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, TextField, Typography } from '@mui/material';
import { useEffect, useId, useState, type ReactNode } from 'react';

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  /** What will happen, in plain words. */
  children?: ReactNode;
  confirmLabel: string;
  /** Marks the confirm button as dangerous (red). */
  destructive?: boolean;
  /** Returns an error message to keep the dialog open with it, or null/undefined when the action went through. */
  onConfirm: () => Promise<string | null | undefined> | string | null | undefined;
  onClose: () => void;
  /** Disables the confirm button (the typed text does not match yet, ...). */
  confirmDisabled?: boolean;
  /** Extra buttons next to Cancel (the ark sync dialog's "Preview"). */
  extraActions?: ReactNode;
}

export function ConfirmDialog({ open, title, children, confirmLabel, destructive = false, onConfirm, onClose, confirmDisabled = false, extraActions }: ConfirmDialogProps) {
  const titleId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setBusy(false);
      setError(null);
    }
  }, [open]);

  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      const failure = await onConfirm();
      if (failure) setError(failure);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={busy ? undefined : onClose} aria-labelledby={titleId} fullWidth maxWidth="sm">
      <DialogTitle id={titleId}>{title}</DialogTitle>
      <DialogContent>
        {children}
        {error ? (
          <Alert severity="error" role="alert" sx={{ mt: 2 }}>
            {error}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        {extraActions}
        <Button onClick={() => void confirm()} variant="contained" color={destructive ? 'error' : 'primary'} disabled={busy || confirmDisabled}>
          {confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export interface TypedConfirmDialogProps extends Omit<ConfirmDialogProps, 'confirmDisabled' | 'extraActions'> {
  /** The exact text the operator must type (a model id, a node id). Compared as typed: no trimming, case-sensitive. */
  expected: string;
}

export function TypedConfirmDialog({ expected, children, ...rest }: TypedConfirmDialogProps) {
  const [typed, setTyped] = useState('');
  useEffect(() => {
    if (rest.open) setTyped('');
  }, [rest.open]);
  return (
    <ConfirmDialog {...rest} destructive confirmDisabled={typed !== expected}>
      {children}
      <Typography variant="body2" sx={{ mt: 2 }}>
        To confirm, type <strong>{expected}</strong> below.
      </Typography>
      <TextField
        value={typed}
        onChange={(event) => setTyped(event.target.value)}
        label={`Type ${expected} to confirm`}
        fullWidth
        size="small"
        margin="dense"
        autoComplete="off"
        slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
      />
    </ConfirmDialog>
  );
}
