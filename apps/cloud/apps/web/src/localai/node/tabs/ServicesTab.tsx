// Services tab (docs/specs/cloud-app.md section 3.11): the five services the node reports (llama, kiwix, whisper, sync,
// drill) with a state chip and since-when. **Restart** exists for llama, kiwix and whisper only (`service.restart`);
// sync and drill are timers the agent runs, never restarted from the cloud.
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import HourglassTopIcon from '@mui/icons-material/HourglassTop';
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked';
import { formatAgo } from '@localai/ui';
import { Alert, Chip, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow } from '@mui/material';
import type { ReactElement } from 'react';

import type { NodeDetail, SnapshotService } from '../../api/types';
import { CommandButton } from '../useNodeActions';
import { useNodeCommands } from '../useNodeCommands';

const RESTARTABLE = new Set(['llama', 'kiwix', 'whisper']);

const STATE: Record<SnapshotService['active_state'], { word: string; color: 'success' | 'warning' | 'error' | 'default'; icon: ReactElement }> = {
  active: { word: 'Running', color: 'success', icon: <CheckCircleIcon /> },
  activating: { word: 'Starting', color: 'warning', icon: <HourglassTopIcon /> },
  deactivating: { word: 'Stopping', color: 'warning', icon: <HourglassTopIcon /> },
  failed: { word: 'Failed', color: 'error', icon: <ErrorIcon /> },
  inactive: { word: 'Stopped', color: 'default', icon: <RadioButtonUncheckedIcon /> },
  not_installed: { word: 'Not installed', color: 'default', icon: <RadioButtonUncheckedIcon /> },
};

export function ServicesTab({ node, now = new Date() }: { node: NodeDetail; now?: Date }) {
  const commands = useNodeCommands();
  const services = node.lastState?.services ?? [];
  if (services.length === 0) return <Alert severity="info">{node.nodeId} has not reported its services yet.</Alert>;
  return (
    <TableContainer component={Paper} variant="outlined" tabIndex={0} role="region" aria-label="Services">
      <Table size="small" aria-label="Services of this node">
        <TableHead>
          <TableRow>
            <TableCell>Service</TableCell>
            <TableCell>State</TableCell>
            <TableCell>Since</TableCell>
            <TableCell>Actions</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {services.map((service) => {
            const state = STATE[service.active_state];
            return (
              <TableRow key={service.name} data-testid={`service-row-${service.name}`}>
                <TableCell>{service.name}</TableCell>
                <TableCell>
                  <Chip size="small" color={state.color} variant={state.color === 'default' ? 'outlined' : 'filled'} icon={state.icon} label={state.word} />
                </TableCell>
                <TableCell>{service.since ? (formatAgo(service.since, now) ?? '—') : '—'}</TableCell>
                <TableCell>
                  {RESTARTABLE.has(service.name) && service.active_state !== 'not_installed' ? (
                    <CommandButton node={node} type="service.restart" subject={service.name} onPress={() => void commands.send('service.restart', { service: service.name })} aria-label={`Restart ${service.name}`}>
                      Restart
                    </CommandButton>
                  ) : null}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </TableContainer>
  );
}
