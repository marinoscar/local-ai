// Logs tab (docs/specs/cloud-app.md section 3.11): pick a unit and a number of lines, **Fetch** queues `logs.tail`,
// and the lines appear in a monospace viewer when the command settles. Lines are rendered as TEXT (never HTML). The
// agent scrubs keys and tokens before the lines leave the computer and the cloud scrubs again; any `[redacted]` the
// viewer shows is counted so the reader knows something was hidden. Results are kept 7 days server-side, so the viewer
// shows the newest fetch of this session only.
import { Alert, Box, TextField, Typography } from '@mui/material';
import { useState } from 'react';

import type { NodeDetail } from '../../api/types';
import { CommandButton, useMay } from '../useNodeActions';
import { useNodeCommands } from '../useNodeCommands';

export const LOG_UNITS = ['agent', 'llama', 'kiwix', 'whisper', 'sync', 'drill'] as const;
export const LOG_LINES = [50, 200, 500] as const;

export function LogsTab({ node }: { node: NodeDetail }) {
  const commands = useNodeCommands();
  const may = useMay();
  const [unit, setUnit] = useState<(typeof LOG_UNITS)[number]>('agent');
  const [lines, setLines] = useState<(typeof LOG_LINES)[number]>(200);

  const latest = commands.latest('logs.tail');
  const result = latest?.status === 'succeeded' ? latest.result : null;
  const text = Array.isArray(result?.lines) ? (result.lines as unknown[]).filter((line): line is string => typeof line === 'string') : null;
  const redacted = text ? text.reduce((count, line) => count + (line.split('[redacted]').length - 1), 0) : 0;
  const canFetch = may.send('logs.tail');

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Typography variant="body2" color="text.secondary">
        Logs are cleaned of keys and tokens before they leave the computer.
      </Typography>

      {canFetch ? (
        <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap', alignItems: 'center' }}>
          <TextField select size="small" label="Unit" value={unit} onChange={(event) => setUnit(event.target.value as (typeof LOG_UNITS)[number])} sx={{ minWidth: 140 }} slotProps={{ select: { native: true } }}>
            {LOG_UNITS.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </TextField>
          <TextField select size="small" label="Lines" value={lines} onChange={(event) => setLines(Number(event.target.value) as (typeof LOG_LINES)[number])} sx={{ minWidth: 110 }} slotProps={{ select: { native: true } }}>
            {LOG_LINES.map((count) => (
              <option key={count} value={count}>
                {count}
              </option>
            ))}
          </TextField>
          <CommandButton node={node} type="logs.tail" subject={unit} variant="contained" onPress={() => void commands.send('logs.tail', { unit, lines })}>
            Fetch
          </CommandButton>
        </Box>
      ) : (
        <Alert severity="info">Fetching logs needs operator access. Ask someone with it to fetch them.</Alert>
      )}

      {text ? (
        <Box>
          <Typography variant="caption" color="text.secondary" data-testid="logs-meta">
            {String(result?.unit ?? unit)} · {text.length} line{text.length === 1 ? '' : 's'}
            {result?.truncated === true ? ' (older lines were cut)' : ''}
            {redacted > 0 ? ` · ${redacted} value${redacted === 1 ? '' : 's'} hidden as [redacted]` : ''}
          </Typography>
          <Box
            component="pre"
            tabIndex={0}
            aria-label="Log lines"
            data-testid="logs-viewer"
            sx={{ m: 0, mt: 0.5, p: 1.5, maxHeight: 420, overflow: 'auto', fontFamily: 'monospace', fontSize: 13, bgcolor: 'action.hover', borderRadius: 1, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}
          >
            {text.length ? text.join('\n') : '(no lines)'}
          </Box>
        </Box>
      ) : latest && latest.status !== 'succeeded' ? null : (
        <Typography variant="body2" color="text.secondary">
          Choose a unit and press Fetch. The lines appear here when the node answers.
        </Typography>
      )}
    </Box>
  );
}
