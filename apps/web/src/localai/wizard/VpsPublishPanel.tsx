// "Publish automatically" (docs/specs/cloud-app.md sections 4.8 and 4.10): enqueues the VPS stack-agent's publish job,
// shows what blocks it in plain words, the step journal of the run, and the optional Unpublish. The journal arrives when
// the stack-agent settles the job (the platform's node API has no progress channel): while it runs the page lists the
// planned steps as waiting and polls the state every 3 seconds.
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import HourglassEmptyIcon from '@mui/icons-material/HourglassEmpty';
import RemoveCircleOutlineIcon from '@mui/icons-material/RemoveCircleOutlineOutlined';
import { Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, List, ListItem, Stack, Tooltip, Typography } from '@mui/material';
import { useEffect, useRef, useState, type ReactElement } from 'react';

import { messageOf, reasonOf, type WizardClient } from './wizardApi';
import type { VpsJournalStep, VpsStatus } from './wizardTypes';
import { usePolled } from './usePolled';

export const VPS_POLL_MS = 3000;

const PUBLISH_STEPS: ReadonlyArray<[string, string]> = [
  ['dns_upsert', 'Create the DNS record'],
  ['dns_wait', 'Wait for DNS to spread'],
  ['bootstrap_vhost', 'Write the HTTP vhost'],
  ['certificate', 'Get the certificate'],
  ['final_vhost', 'Write the final HTTPS vhost'],
  ['validate', 'Check /health and /v1/models'],
];
const UNPUBLISH_STEPS: ReadonlyArray<[string, string]> = [
  ['vhost_remove', 'Remove the vhost'],
  ['dns_delete', 'Delete the DNS record'],
  ['certificate_kept', 'Leave the certificate to expire'],
];

const STEP_CHIP: Record<VpsJournalStep['status'] | 'waiting', { label: string; color: 'default' | 'success' | 'error'; icon: ReactElement }> = {
  waiting: { label: 'Waiting', color: 'default', icon: <HourglassEmptyIcon /> },
  done: { label: 'Done', color: 'success', icon: <CheckCircleIcon /> },
  skipped: { label: 'Skipped', color: 'default', icon: <RemoveCircleOutlineIcon /> },
  failed: { label: 'Failed', color: 'error', icon: <ErrorIcon /> },
};

const RUN_LABEL: Record<VpsStatus['run']['state'], string> = { idle: '', queued: 'Queued', running: 'Running', succeeded: 'Finished', failed: 'Failed' };

function Journal({ status }: { status: VpsStatus }) {
  const { run } = status;
  if (run.action === null) return null;
  const planned = run.action === 'publish' ? PUBLISH_STEPS : UNPUBLISH_STEPS;
  const active = run.state === 'queued' || run.state === 'running';
  const byName = new Map(run.steps.map((step) => [step.name, step]));
  const label = run.action === 'publish' ? 'Publish journal' : 'Unpublish journal';
  return (
    <List aria-label={label} disablePadding data-testid="vps-journal">
      {planned.map(([name, title]) => {
        const step = active ? undefined : byName.get(name);
        // A finished run that stopped early leaves its later steps "Not run".
        const chip = active || !step ? STEP_CHIP.waiting : STEP_CHIP[step.status];
        const chipLabel = active ? 'Waiting' : !step ? 'Not run' : chip.label;
        return (
          <ListItem key={name} divider disableGutters sx={{ display: 'block', py: 1 }} data-testid={`vps-step-${name}`}>
            <Stack direction="row" spacing={1.25} sx={{ alignItems: 'center' }}>
              <Chip size="small" icon={chip.icon} color={chip.color} label={chipLabel} variant={step?.status === 'done' || step?.status === 'failed' ? 'filled' : 'outlined'} />
              <Typography sx={{ minWidth: 0, overflowWrap: 'anywhere' }}>{title}</Typography>
            </Stack>
            {step ? (
              <Typography variant="body2" color={step.status === 'failed' ? 'error' : 'text.secondary'} sx={{ mt: 0.5, ml: 0.5, overflowWrap: 'anywhere' }}>
                {step.detail}
              </Typography>
            ) : null}
          </ListItem>
        );
      })}
    </List>
  );
}

export interface VpsPublishPanelProps {
  client: WizardClient;
  nodeId: string;
  /** Called when a run settles, so the checklist below shows steps 2 to 5 as the run left them. */
  onSettled: () => void;
}

export function VpsPublishPanel({ client, nodeId, onSettled }: VpsPublishPanelProps) {
  const poll = usePolled<VpsStatus>(() => client.vpsStatus(nodeId), VPS_POLL_MS);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const status = poll.data;
  const state = status?.run.state ?? 'idle';
  const running = state === 'queued' || state === 'running';

  // Tell the checklist when a run we watched settles.
  const previous = useRef<string>('idle');
  useEffect(() => {
    const was = previous.current;
    previous.current = state;
    if ((was === 'queued' || was === 'running') && (state === 'succeeded' || state === 'failed')) onSettled();
  }, [state, onSettled]);

  const request = async (action: 'publish' | 'unpublish') => {
    setBusy(true);
    setError(null);
    try {
      await (action === 'publish' ? client.vpsPublish(nodeId) : client.vpsUnpublish(nodeId));
      poll.refresh();
    } catch (caught) {
      setError(reasonOf(caught) ? messageOf(caught, 'That did not work. Try again.') : 'That did not work. Try again.');
      poll.refresh();
    } finally {
      setBusy(false);
    }
  };

  const publishBlockers = status?.publish.blockers ?? [];
  const publishDisabled = busy || running || status === null || !status.publish.available;
  const unpublishDisabled = busy || running || status === null || !status.unpublish.available;
  const reason = running ? 'A run is already in progress.' : publishBlockers.map((b) => b.message).join(' ');

  return (
    <Box data-testid="vps-panel" sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 2 }}>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1.5} sx={{ justifyContent: 'space-between', alignItems: { sm: 'flex-start' } }}>
        <Box sx={{ maxWidth: 560 }}>
          <Typography variant="subtitle1" component="h2">
            Publish automatically
          </Typography>
          <Typography color="text.secondary" variant="body2">
            The VPS helper does steps 2 to 5 for {status?.host ?? nodeId}: the DNS record, the certificate and the vhost. You can still do them by hand below.
          </Typography>
        </Box>
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
          <Tooltip title={publishDisabled && reason ? reason : ''} describeChild>
            <span tabIndex={publishDisabled ? 0 : -1}>
              <Button variant="contained" disabled={publishDisabled} onClick={() => void request('publish')} startIcon={busy || running ? <CircularProgress size={14} color="inherit" aria-hidden /> : undefined}>
                Publish automatically
              </Button>
            </span>
          </Tooltip>
          <Button variant="outlined" color="warning" disabled={unpublishDisabled} onClick={() => setConfirming(true)}>
            Unpublish…
          </Button>
        </Stack>
      </Stack>

      {poll.error && !status ? (
        <Alert severity="error" sx={{ mt: 1.5 }} action={<Button color="inherit" size="small" onClick={poll.refresh}>Try again</Button>}>
          Could not load the automatic publishing status.
        </Alert>
      ) : null}

      {status && !status.publish.available && !running ? (
        <Alert severity="info" sx={{ mt: 1.5 }} data-testid="vps-blockers">
          <Typography variant="body2" component="div">
            Automatic publishing is not ready yet:
          </Typography>
          <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
            {status.publish.blockers.map((blocker) => (
              <li key={blocker.code}>{blocker.message}</li>
            ))}
          </Box>
        </Alert>
      ) : null}

      {error ? (
        <Alert severity="error" sx={{ mt: 1.5 }}>
          {error}
        </Alert>
      ) : null}

      {status && status.run.action !== null ? (
        <Box sx={{ mt: 1.5 }}>
          <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 0.5 }} role="status" aria-live="polite">
            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              {status.run.action === 'publish' ? 'Last publish' : 'Last unpublish'}: {RUN_LABEL[status.run.state]}
            </Typography>
            {running ? <CircularProgress size={14} aria-label="Working" /> : null}
          </Stack>
          {running ? (
            <Typography variant="body2" color="text.secondary" sx={{ mb: 0.5 }}>
              The helper reports each step when it finishes the job; this page checks every few seconds.
            </Typography>
          ) : null}
          {status.run.error ? (
            <Alert severity="error" sx={{ mb: 1 }}>
              {status.run.error}
            </Alert>
          ) : null}
          {state === 'succeeded' && status.run.action === 'publish' && status.run.certificateExpiresAt ? (
            <Alert severity="success" sx={{ mb: 1 }}>
              {status.host} is published. The certificate renews by itself; it ends on {status.run.certificateExpiresAt.slice(0, 10)}.
            </Alert>
          ) : null}
          <Journal status={status} />
        </Box>
      ) : null}

      <Dialog open={confirming} onClose={() => setConfirming(false)} aria-labelledby="vps-unpublish-title">
        <DialogTitle id="vps-unpublish-title">Unpublish {status?.host ?? nodeId}?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            The helper removes the vhost from the proxy and deletes the DNS record. The certificate is left to expire. The node itself is not touched and keeps working on the tailnet.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirming(false)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={() => {
              setConfirming(false);
              void request('unpublish');
            }}
          >
            Unpublish
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
