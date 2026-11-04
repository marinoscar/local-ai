// One command on the page: what it is, where it stands (queued / running / done / failed), how long, its result or the
// plain-language fix of its error, and Cancel while it is still queued (docs/specs/cloud-app.md section 3.11).
import CancelIcon from '@mui/icons-material/Cancel';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import CloseIcon from '@mui/icons-material/Close';
import ErrorIcon from '@mui/icons-material/Error';
import HourglassTopIcon from '@mui/icons-material/HourglassTop';
import PlayCircleIcon from '@mui/icons-material/PlayCircle';
import { Box, Button, IconButton, Typography } from '@mui/material';
import { useEffect, useState, type ReactElement } from 'react';

import type { CommandView } from '../api/types';
import { FixText } from './FixText';
import { describeCommand, elapsedText, failureText, isActive, resultSummary, statusText } from './commandText';

export interface CommandStatusLineProps {
  command: CommandView;
  /** Shows **Cancel** (only a queued command can be cancelled, and only by someone allowed to send its type). */
  canCancel: boolean;
  onCancel: (id: string) => void;
  onDismiss: (id: string) => void;
  /** The instant elapsed time counts from (tests). Default: a live clock while the command is active. */
  now?: Date;
}

const ICON: Record<CommandView['status'], ReactElement> = {
  queued: <HourglassTopIcon color="action" fontSize="small" />,
  running: <PlayCircleIcon color="primary" fontSize="small" />,
  succeeded: <CheckCircleIcon color="success" fontSize="small" />,
  failed: <ErrorIcon color="error" fontSize="small" />,
  expired: <ErrorIcon color="warning" fontSize="small" />,
  cancelled: <CancelIcon color="disabled" fontSize="small" />,
};

function useNow(active: boolean, fixed?: Date): Date {
  const [now, setNow] = useState(() => fixed ?? new Date());
  useEffect(() => {
    if (fixed || !active) return undefined;
    const timer = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(timer);
  }, [active, fixed]);
  return fixed ?? now;
}

export function CommandStatusLine({ command, canCancel, onCancel, onDismiss, now: fixedNow }: CommandStatusLineProps) {
  const active = isActive(command);
  const now = useNow(active, fixedNow);
  const label = describeCommand(command.type, command.params);
  const failed = command.status === 'failed' || command.status === 'expired';
  const problem = failed ? failureText(command) : null;
  const summary = command.status === 'succeeded' ? resultSummary(command) : null;

  return (
    <Box
      component="li"
      data-testid="command-line"
      data-status={command.status}
      sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, py: 0.75, borderBottom: 1, borderColor: 'divider', listStyle: 'none' }}
    >
      <Box sx={{ pt: 0.25 }} aria-hidden>
        {ICON[command.status]}
      </Box>
      <Box sx={{ flex: 1, minWidth: 0 }}>
        <Typography variant="body2" sx={{ overflowWrap: 'anywhere' }}>
          <strong>{label}</strong>
          {' · '}
          <span data-testid="command-status">{statusText(command)}</span>
          {summary ? ` (${summary})` : ''}
          {' · '}
          {elapsedText(command, now)}
        </Typography>
        {problem ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 0.25, overflowWrap: 'anywhere' }} data-testid="command-problem">
            {problem.meaning ? `${problem.meaning}. ` : ''}
            <FixText text={problem.fix} />
            {problem.code ? ` (${problem.code})` : ''}
          </Typography>
        ) : null}
      </Box>
      {command.status === 'queued' && canCancel ? (
        <Button size="small" color="inherit" variant="outlined" onClick={() => onCancel(command.id)} aria-label={`Cancel: ${label}`}>
          Cancel
        </Button>
      ) : null}
      {!active ? (
        <IconButton size="small" onClick={() => onDismiss(command.id)} aria-label={`Dismiss: ${label}`}>
          <CloseIcon fontSize="inherit" />
        </IconButton>
      ) : null}
    </Box>
  );
}
