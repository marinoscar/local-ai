// Keys tab (docs/specs/cloud-app.md sections 3.8 and 3.11): the llama key as a HINT only (`ab12…9z0f`, first and last
// four), when it was reported, and **Rotate key…** (lab:admin, `key.rotate`); the node's `nod_` credential as a prefix
// and a last-used time, and **Replace credential…** (lab:admin, `POST …/credential`). A key or token is never shown
// except the NEW node credential, once, in the dialog that the replace opens; it lives only in that dialog's state, so
// closing it or reloading the page never brings it back.
import { formatAgo } from '@localai/ui';
import { Alert, Box, Button, DialogContentText, Paper, Typography } from '@mui/material';
import { useState } from 'react';

import { replaceNodeCredential } from '../../api/nodes';
import type { NodeDetail, ReplacedCredential } from '../../api/types';
import { ConfirmDialog } from '../ConfirmDialog';
import { refusalText } from '../useNodeCommands';
import { useNodeCommands } from '../useNodeCommands';
import { CommandButton, useMay } from '../useNodeActions';
import { formatWhen } from '../format';
import { TokenDialog } from '../TokenDialog';
import { FixText } from '../FixText';
import { registryRow } from '../../doctorFix';

export function KeysTab({ node, now = new Date(), onChanged }: { node: NodeDetail; now?: Date; onChanged: () => void }) {
  const commands = useNodeCommands();
  const may = useMay();
  const [dialog, setDialog] = useState<'rotate' | 'replace' | null>(null);
  const [issued, setIssued] = useState<ReplacedCredential | null>(null);
  const missingFix = registryRow('LAI-CLD-026');

  return (
    <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' } }}>
      <Paper variant="outlined" component="section" aria-label="Llama key" sx={{ p: 2, display: 'flex', flexDirection: 'column', gap: 1 }}>
        <Typography variant="h6" component="h2">
          Llama key
        </Typography>
        {node.keys.keyHint ? (
          <Typography data-testid="key-hint">
            Key hint: <code>{node.keys.keyHint}</code>
          </Typography>
        ) : (
          <Alert severity="warning" data-testid="key-missing">
            The cloud has no key for this node yet, so the gateway cannot use it. {missingFix ? <FixText text={missingFix.fix} /> : null} (LAI-CLD-026)
          </Alert>
        )}
        <Typography variant="body2" color="text.secondary">
          Reported {node.keys.keyReportedAt ? `${formatWhen(node.keys.keyReportedAt)} (${formatAgo(node.keys.keyReportedAt, now)})` : 'never'}. Only the hint is shown; the key itself is never displayed.
        </Typography>
        <Box>
          <CommandButton node={node} type="key.rotate" onPress={() => setDialog('rotate')}>
            Rotate key…
          </CommandButton>
        </Box>
      </Paper>

      <Paper variant="outlined" component="section" aria-label="Node credential" sx={{ p: 2, display: 'flex', flexDirection: 'column', gap: 1 }}>
        <Typography variant="h6" component="h2">
          Node credential
        </Typography>
        {node.keys.credential ? (
          <>
            <Typography data-testid="credential-prefix">
              Credential: <code>nod_{node.keys.credential.prefix}…</code>
            </Typography>
            <Typography variant="body2" color="text.secondary">
              Last used {node.keys.credential.lastUsedAt ? `${formatAgo(node.keys.credential.lastUsedAt, now)}` : 'never'} · created {formatWhen(node.keys.credential.createdAt)}.
            </Typography>
          </>
        ) : (
          <Typography color="text.secondary">No credential is bound to this node.</Typography>
        )}
        {may.admin ? (
          <Box>
            <Button size="small" variant="outlined" color="warning" onClick={() => setDialog('replace')}>
              Replace credential…
            </Button>
          </Box>
        ) : null}
      </Paper>

      <ConfirmDialog
        open={dialog === 'rotate'}
        title={`Rotate the key of ${node.nodeId}?`}
        confirmLabel="Rotate key"
        onClose={() => setDialog(null)}
        onConfirm={async () => {
          const outcome = await commands.send('key.rotate', {}, { silent: true });
          if (outcome.ok) setDialog(null);
          return outcome.ok ? null : outcome.message;
        }}
      >
        <DialogContentText>
          The node makes a new llama key, restarts its AI service with it and sends the new key to the cloud. Anything that calls {node.nodeId} directly with the old key must be updated. Open WebUI uses the gateway, so usually nothing needs changing.
        </DialogContentText>
      </ConfirmDialog>

      <ConfirmDialog
        open={dialog === 'replace'}
        title={`Replace the credential of ${node.nodeId}?`}
        confirmLabel="Replace credential"
        destructive
        onClose={() => setDialog(null)}
        onConfirm={async () => {
          try {
            const created = await replaceNodeCredential(node.nodeId);
            setIssued(created);
            setDialog(null);
            onChanged();
            return null;
          } catch (error) {
            return refusalText(error, node.nodeId).message;
          }
        }}
      >
        <DialogContentText>
          The current credential stops working at once, so the cloud loses contact with {node.nodeId} until someone runs <code>sudo aictl cloud enroll</code> on the computer with the new one. The new credential is shown once.
        </DialogContentText>
      </ConfirmDialog>

      <TokenDialog credential={issued} nodeId={node.nodeId} onClose={() => setIssued(null)} />
    </Box>
  );
}
