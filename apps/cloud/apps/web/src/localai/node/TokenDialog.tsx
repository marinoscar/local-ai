// Shows a freshly minted node credential ONCE (docs/specs/cloud-app.md section 3.9, issue #86): the token, a copy
// button and the re-enroll steps. The token exists only in the props of this dialog; when it closes the parent drops
// it, and nothing stores it, so a reload cannot show it again.
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, Typography } from '@mui/material';
import { useId, useState } from 'react';

import type { ReplacedCredential } from '../api/types';

export function TokenDialog({ credential, nodeId, onClose }: { credential: ReplacedCredential | null; nodeId: string; onClose: () => void }) {
  const titleId = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (!credential) return;
    try {
      await navigator.clipboard.writeText(credential.token);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <Dialog open={credential !== null} onClose={onClose} aria-labelledby={titleId} fullWidth maxWidth="sm">
      <DialogTitle id={titleId}>New credential for {nodeId}</DialogTitle>
      <DialogContent>
        <Alert severity="warning" sx={{ mb: 2 }}>
          This is the only time the credential is shown. Copy it now; it cannot be shown again.
        </Alert>
        <Box
          component="code"
          data-testid="new-credential"
          sx={{ display: 'block', p: 1.5, bgcolor: 'action.hover', borderRadius: 1, fontFamily: 'monospace', overflowWrap: 'anywhere', userSelect: 'all' }}
        >
          {credential?.token}
        </Box>
        <Box sx={{ mt: 1 }}>
          <Button size="small" onClick={() => void copy()}>
            {copied ? 'Copied' : 'Copy'}
          </Button>
        </Box>
        <Typography variant="subtitle2" sx={{ mt: 2 }}>
          Then, on the computer:
        </Typography>
        <Box component="ol" sx={{ mt: 0.5, pl: 3 }}>
          {(credential?.reenroll ?? []).map((step) => (
            <li key={step}>
              <Typography variant="body2">{step}</Typography>
            </li>
          ))}
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} variant="contained">
          I have copied it
        </Button>
      </DialogActions>
    </Dialog>
  );
}
