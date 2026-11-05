// Edit one model's policy (docs/specs/gateway.md section 3.8; gateway:admin). The form checks what the API checks, and
// shows the API's own message when it refuses (an alias another model owns, for example). Closing never saves.
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Switch, TextField, Typography } from '@mui/material';
import { useId, useState } from 'react';

import { checkDraft, draftOf, type PolicyDraft } from './modelsFormat';
import type { ModelRow, PolicyInput } from './modelsTypes';

export interface ModelPolicyDialogProps {
  row: ModelRow;
  /** Every catalog model id: an alias may not equal one. */
  catalogIds: readonly string[];
  save: (modelId: string, input: PolicyInput) => Promise<unknown>;
  /** Called after the API accepted the policy. */
  onSaved: () => void;
  onClose: () => void;
}

export function ModelPolicyDialog({ row, catalogIds, save, onSaved, onClose }: ModelPolicyDialogProps) {
  const titleId = useId();
  const [draft, setDraft] = useState<PolicyDraft>(() => draftOf(row));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);

  const checked = checkDraft(draft, catalogIds);
  const shown = touched ? checked.errors : {};
  const set = <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => setDraft((previous) => ({ ...previous, [key]: value }));

  const submit = async () => {
    setTouched(true);
    if (!checked.input) return;
    setBusy(true);
    setError(null);
    try {
      await save(row.modelId, checked.input);
      onSaved();
    } catch (caught) {
      setError(caught instanceof Error && caught.message ? caught.message : 'Could not save the policy.');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open onClose={() => (busy ? undefined : onClose())} aria-labelledby={titleId} fullWidth maxWidth="sm">
      <DialogTitle id={titleId}>{`Policy for ${row.displayName}`}</DialogTitle>
      <DialogContent sx={{ display: 'flex', flexDirection: 'column', gap: 2, pt: 1 }}>
        <Typography variant="body2" color="text.secondary">
          <code>{row.modelId}</code>
        </Typography>
        {error ? <Alert severity="error">{error}</Alert> : null}
        <FormControlLabel
          control={<Switch checked={draft.enabled} onChange={(event) => set('enabled', event.target.checked)} />}
          label="Offered on /v1 (turn off to hide the model and its aliases)"
        />
        <TextField
          label="Aliases"
          value={draft.aliases}
          onChange={(event) => set('aliases', event.target.value)}
          error={Boolean(shown.aliases)}
          helperText={shown.aliases ?? 'Other names clients may use, separated by commas. Each alias belongs to one model only.'}
          placeholder="lab-coder"
        />
        <FormControlLabel
          control={<Switch checked={draft.onDemandLoad} onChange={(event) => set('onDemandLoad', event.target.checked)} />}
          label="Load on demand (a request for it loads it on a node that has it, never replacing a model that is answering)"
        />
        <TextField
          label="Wait for a load (seconds)"
          type="number"
          value={draft.loadWaitS}
          onChange={(event) => set('loadWaitS', event.target.value)}
          error={Boolean(shown.loadWaitS)}
          helperText={shown.loadWaitS ?? 'How long a request waits for the model to load. From 10 to 600.'}
          slotProps={{ htmlInput: { min: 10, max: 600, step: 1 } }}
        />
        <TextField
          label="First-byte timeout (seconds)"
          type="number"
          value={draft.firstByteTimeoutS}
          onChange={(event) => set('firstByteTimeoutS', event.target.value)}
          error={Boolean(shown.firstByteTimeoutS)}
          helperText={shown.firstByteTimeoutS ?? 'Leave empty to use the gateway setting.'}
          slotProps={{ htmlInput: { min: 1, max: 3600, step: 1 } }}
        />
        <TextField
          label="Preferred nodes"
          value={draft.preferredNodes}
          onChange={(event) => set('preferredNodes', event.target.value)}
          error={Boolean(shown.preferredNodes)}
          helperText={shown.preferredNodes ?? 'In order of preference, separated by commas, for example ai-n2, ai-n1.'}
          placeholder="ai-n2"
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button variant="contained" onClick={() => void submit()} disabled={busy}>
          Save
        </Button>
      </DialogActions>
    </Dialog>
  );
}
