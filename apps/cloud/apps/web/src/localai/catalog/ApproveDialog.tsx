// "Approve 2026.11.03-1 for all nodes?" (docs/specs/cloud-app.md 4.6). Shows the diff summary against the approved
// version before the confirm button, and the "Push to nodes now" checkbox (`notifyNodes: true`). The API refuses a
// version that is not `valid` (409); the page enables the button only for one, and a refusal is shown here.
import { Alert, Box, Button, Checkbox, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Typography } from '@mui/material';
import { useState } from 'react';

import { errorMessage, type ApproveResponse, type CatalogApi } from '../api/catalog';
import { summaryText } from './catalogDiff';
import { useCatalogDiff } from './useCatalog';

export interface ApproveDialogProps {
  client: CatalogApi;
  /** The version to approve; null closes the dialog. */
  version: string | null;
  onClose(): void;
  onApproved(result: ApproveResponse, version: string): void;
}

export function ApproveDialog({ client, version, onClose, onApproved }: ApproveDialogProps) {
  // Mounted only while a version is chosen, so the checkbox and any earlier refusal reset for the next one.
  return version === null ? null : <ApproveDialogBody client={client} version={version} onClose={onClose} onApproved={onApproved} />;
}

function ApproveDialogBody({ client, version, onClose, onApproved }: Omit<ApproveDialogProps, 'version'> & { version: string }) {
  const diff = useCatalogDiff(client, version, 'approved');
  const [notifyNodes, setNotifyNodes] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const titleId = 'approve-dialog-title';

  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      const result = await client.approve(version, notifyNodes);
      onApproved(result, version);
    } catch (caught) {
      setError(errorMessage(caught, 'Could not approve this version. Reload and try again.'));
      setBusy(false);
    }
  };

  const changed = diff.files.filter((file) => file.change !== 'unchanged');
  return (
    <Dialog open onClose={busy ? undefined : onClose} aria-labelledby={titleId} fullWidth maxWidth="sm">
      <DialogTitle id={titleId}>Approve {version} for all nodes?</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.5 }}>
          <Typography variant="body2" color="text.secondary">
            Enrolled nodes download the approved catalog. The node checks it again before it swaps, so a node never loses the catalog it has.
          </Typography>
          <Box data-testid="approve-diff-summary" aria-live="polite">
            {diff.loading ? (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                <CircularProgress size={16} aria-hidden="true" />
                <Typography variant="body2">Comparing with the approved catalog…</Typography>
              </Box>
            ) : diff.error ? (
              <Alert severity="warning">Could not compare with the approved catalog. You can still approve.</Alert>
            ) : (
              <>
                <Typography variant="subtitle2" component="p">
                  {diff.against === null ? 'No catalog is approved yet; this will be the first.' : `Changes since ${diff.against}`}
                </Typography>
                <Typography variant="body2">{summaryText(diff.summary)}</Typography>
                {changed.length > 0 ? (
                  <Box component="ul" sx={{ m: 0, mt: 0.5, pl: 2.5 }} aria-label="Changed files">
                    {changed.map((file) => (
                      <li key={file.path}>
                        <Typography variant="body2" component="span" sx={{ fontFamily: 'monospace' }}>
                          {file.path}
                        </Typography>{' '}
                        <Typography variant="caption" color="text.secondary" component="span">
                          {file.change} · +{file.added} -{file.removed}
                        </Typography>
                      </li>
                    ))}
                  </Box>
                ) : null}
              </>
            )}
          </Box>
          <FormControlLabel
            control={<Checkbox checked={notifyNodes} onChange={(event) => setNotifyNodes(event.target.checked)} disabled={busy} />}
            label="Push to nodes now"
          />
          {error ? <Alert severity="error">{error}</Alert> : null}
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button variant="contained" onClick={() => void confirm()} disabled={busy || diff.loading}>
          {busy ? 'Approving…' : 'Approve'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
