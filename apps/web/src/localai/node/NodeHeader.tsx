// The header of the node detail page (docs/specs/cloud-app.md section 3.11): back link, name and id, the health, mode and
// connectivity chips, the profile / agent / llama line, how fresh the data is, and the node-wide buttons:
// **Refresh status** (lab:operate), **Go offline…** and **Update agent…** (lab:admin). Buttons the user may not use are
// not rendered; buttons the node cannot take right now are disabled and the banner says why.
import ArrowBackIcon from '@mui/icons-material/ArrowBack';
import { HealthChip, ModeChip, formatAgo } from '@localai/ui';
import { Alert, Box, Button, Chip, DialogContentText, Typography } from '@mui/material';
import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import type { NodeDetail } from '../api/types';
import { ConfirmDialog } from './ConfirmDialog';
import { agentUpdateState, gateFor } from './commandPolicy';
import { CommandButton } from './useNodeActions';
import { useNodeCommands } from './useNodeCommands';

export function NodeHeader({ node, now = new Date() }: { node: NodeDetail; now?: Date }) {
  const commands = useNodeCommands();
  const [dialog, setDialog] = useState<'offline' | 'update' | null>(null);
  const update = agentUpdateState(node);
  const gate = gateFor(node, 'status.refresh');
  const llama = node.lastState?.llama;
  const meta = [
    node.settings.profile ? `profile ${node.settings.profile}` : null,
    node.agentVersion ? `agent ${node.agentVersion}` : null,
    llama?.build ? `llama ${llama.build}` : null,
    llama?.router ? `router ${llama.router.replace('_', ' ')}` : null,
    llama?.models_max ? `models_max ${llama.models_max}` : null,
  ].filter(Boolean);
  const seen = formatAgo(node.lastStateAt, now);

  return (
    <Box component="header" sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
      <Box>
        <Button component={RouterLink} to="/lab" size="small" startIcon={<ArrowBackIcon />} aria-label="Back to the lab">
          Lab
        </Button>
      </Box>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5 }}>
        <Typography id="node-title" variant="h4" component="h1" sx={{ overflowWrap: 'anywhere' }}>
          {node.nodeId} · {node.displayName}
        </Typography>
        <HealthChip health={node.health} mode={node.mode} phase={node.phase} />
        <ModeChip mode={node.mode} />
        {node.settings.connectivity ? <Chip size="small" variant="outlined" label={`connectivity: ${node.settings.connectivity}`} /> : null}
      </Box>
      {meta.length > 0 ? (
        <Typography variant="body2" color="text.secondary" data-testid="node-meta">
          {meta.join(' · ')}
        </Typography>
      ) : null}

      {!gate.allowed && (gate.code === 'LAI-CLD-034' || gate.code === 'LAI-CLD-035') ? (
        <Alert severity="info" data-testid="refusal-banner">
          {gate.reason} ({gate.code})
        </Alert>
      ) : null}

      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1 }}>
        <CommandButton node={node} type="status.refresh" variant="outlined" onPress={() => void commands.send('status.refresh', {})}>
          Refresh status
        </CommandButton>
        <CommandButton node={node} type="mode.offline" color="warning" onPress={() => setDialog('offline')}>
          Go offline…
        </CommandButton>
        {update.visible ? (
          <CommandButton node={node} type="agent.update" disabledReason={update.enabled ? null : update.reason} onPress={() => setDialog('update')}>
            {update.from && update.to ? `Update agent ${update.from} → ${update.to}…` : 'Update agent…'}
          </CommandButton>
        ) : null}
        <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto' }} data-testid="last-update">
          {seen ? `last update ${seen}` : 'no update received yet'}
        </Typography>
      </Box>

      <ConfirmDialog
        open={dialog === 'offline'}
        title={`Take ${node.nodeId} offline?`}
        confirmLabel="Go offline"
        destructive
        onClose={() => setDialog(null)}
        onConfirm={async () => {
          const outcome = await commands.send('mode.offline', {}, { silent: true });
          if (outcome.ok) setDialog(null);
          return outcome.ok ? null : outcome.message;
        }}
      >
        <DialogContentText>
          {node.nodeId} will stop taking commands from the cloud and cut its internet link. Only someone at the node can bring it back online.
        </DialogContentText>
      </ConfirmDialog>

      <ConfirmDialog
        open={dialog === 'update'}
        title="Update the agent?"
        confirmLabel="Update agent"
        onClose={() => setDialog(null)}
        onConfirm={async () => {
          const outcome = await commands.send('agent.update', { version: update.to }, { silent: true });
          if (outcome.ok) setDialog(null);
          return outcome.ok ? null : outcome.message;
        }}
      >
        <DialogContentText>
          Update {node.nodeId} from {update.from} to {update.to}? The agent restarts; models keep running.
        </DialogContentText>
      </ConfirmDialog>
    </Box>
  );
}
