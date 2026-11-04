// Admin → VPS publishing (docs/specs/cloud-app.md section 4.10): the IAM key the VPS stack-agent uses to publish a node's
// DNS record. WRITE-ONLY: the fields are always empty, the page shows whether a key is stored and its masked hint, and
// nothing here ever reads the key back. The permission is the string the API routes enforce (`lab:admin`).
import { SecretField } from '@marinoscar/platform-web/credentials/ui';
import { Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Stack, TextField, Typography } from '@mui/material';
import { useCallback, useEffect, useState, type FormEvent } from 'react';

import { defaultRoute53Client, type Route53Client, type Route53KeyStatus } from './route53Api';

export const IAM_POLICY_EXAMPLE = `{ "Version": "2012-10-17", "Statement": [
  { "Effect": "Allow", "Action": "route53:ChangeResourceRecordSets", "Resource": "arn:aws:route53:::hostedzone/<ROUTE53_ZONE_ID>" },
  { "Effect": "Allow", "Action": "route53:GetChange", "Resource": "arn:aws:route53:::change/*" } ] }`;

const messageOf = (error: unknown): string => (error instanceof Error && error.message ? error.message : 'That did not work. Try again.');

export function Route53KeyPage({ client = defaultRoute53Client }: { client?: Route53Client }) {
  const [status, setStatus] = useState<Route53KeyStatus | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [accessKeyId, setAccessKeyId] = useState('');
  const [secretAccessKey, setSecretAccessKey] = useState('');
  const [hostedZoneId, setHostedZoneId] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);

  const load = useCallback(async () => {
    try {
      setStatus(await client.status());
      setLoadError(false);
    } catch {
      setLoadError(true);
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  const complete = accessKeyId.trim() !== '' && secretAccessKey.trim() !== '' && hostedZoneId.trim() !== '';

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (!complete) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      setStatus(await client.save({ accessKeyId: accessKeyId.trim(), secretAccessKey: secretAccessKey.trim(), hostedZoneId: hostedZoneId.trim() }));
      // The key leaves this page's state the moment it is saved.
      setAccessKeyId('');
      setSecretAccessKey('');
      setHostedZoneId('');
      setNotice('The key is saved. It cannot be shown again.');
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setConfirming(false);
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      setStatus(await client.remove());
      setNotice('The key is removed. Automatic publishing is off until a new key is saved.');
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ maxWidth: 720 }}>
      <Typography variant="h4" component="h1">
        VPS publishing
      </Typography>
      <Typography color="text.secondary" sx={{ mt: 1, mb: 2 }}>
        The VPS helper publishes a new node&rsquo;s name in DNS. It needs an AWS key that can change one record in the lab&rsquo;s zone and nothing else. The key is kept encrypted and is never shown again.
      </Typography>

      {loadError ? (
        <Alert severity="error" action={<Button color="inherit" size="small" onClick={() => void load()}>Try again</Button>} sx={{ mb: 2 }}>
          Could not load the key status.
        </Alert>
      ) : null}

      {status === null && !loadError ? <CircularProgress aria-label="Loading" /> : null}

      {status ? (
        <Stack direction="row" spacing={1.5} sx={{ alignItems: 'center', mb: 2 }} role="status">
          <Chip color={status.configured ? 'success' : 'default'} variant={status.configured ? 'filled' : 'outlined'} label={status.configured ? 'Key stored' : 'No key stored'} />
          {status.configured ? (
            <Typography variant="body2" color="text.secondary">
              Access key {status.accessKeyHint ?? ''}
              {status.updatedAt ? `, saved ${status.updatedAt.slice(0, 10)}` : ''}
            </Typography>
          ) : null}
        </Stack>
      ) : null}

      {notice ? (
        <Alert severity="success" sx={{ mb: 2 }}>
          {notice}
        </Alert>
      ) : null}
      {error ? (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      ) : null}

      <Box component="form" onSubmit={(event) => void save(event)} noValidate autoComplete="off" aria-label="Route53 key">
        <Stack spacing={2}>
          <SecretField
            label="Access key id"
            value={accessKeyId}
            onChange={setAccessKeyId}
            saved={status?.configured ? { hint: status.accessKeyHint ?? '', updatedAt: status.updatedAt } : null}
            noun="access key id"
            emptyHelp="Starts with AKIA."
            disabled={busy}
            name="accessKeyId"
          />
          <SecretField label="Secret access key" value={secretAccessKey} onChange={setSecretAccessKey} noun="secret access key" emptyHelp="Shown by AWS once, when the key is created." disabled={busy} name="secretAccessKey" />
          <TextField
            label="Hosted zone id"
            value={hostedZoneId}
            onChange={(event) => setHostedZoneId(event.target.value)}
            helperText={status?.configured ? `Saved ${status.hostedZoneHint ?? ''}. Type a new one to replace it.` : 'The Route53 zone of marin.cr; it starts with Z.'}
            disabled={busy}
            autoComplete="off"
            slotProps={{ htmlInput: { name: 'hostedZoneId', spellCheck: false } }}
          />
          <Stack direction="row" spacing={1}>
            <Button type="submit" variant="contained" disabled={busy || !complete}>
              {status?.configured ? 'Replace key' : 'Save key'}
            </Button>
            {status?.configured ? (
              <Button color="warning" variant="outlined" disabled={busy} onClick={() => setConfirming(true)}>
                Remove key
              </Button>
            ) : null}
          </Stack>
          <Typography variant="body2" color="text.secondary">
            To replace the key, fill in all three fields.
          </Typography>
        </Stack>
      </Box>

      <Box sx={{ mt: 3 }}>
        <Typography variant="subtitle1" component="h2">
          The AWS policy for this key
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          Create a user with only this policy (replace the placeholder with your zone id). The runbook walks through it.
        </Typography>
        <Box component="pre" tabIndex={0} aria-label="IAM policy example" sx={{ m: 0, p: 1.5, overflowX: 'auto', bgcolor: 'action.hover', borderRadius: 1, fontSize: 13 }}>
          {IAM_POLICY_EXAMPLE}
        </Box>
      </Box>

      <Dialog open={confirming} onClose={() => setConfirming(false)} aria-labelledby="route53-remove-title">
        <DialogTitle id="route53-remove-title">Remove the Route53 key?</DialogTitle>
        <DialogContent>
          <DialogContentText>Automatic publishing stops working until a new key is saved. Nodes that are already published are not changed.</DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirming(false)}>Cancel</Button>
          <Button color="warning" variant="contained" onClick={() => void remove()}>
            Remove key
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
