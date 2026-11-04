import { Box, LinearProgress, Typography } from '@mui/material';

import { formatGiB, vramPercent } from './format';

export interface VramBarProps {
  usedMib: number;
  totalMib: number;
}

/** "VRAM [bar] 61 GB / 96 GB". Turns amber from 90% and red from 98%; the numbers are always printed. */
export function VramBar({ usedMib, totalMib }: VramBarProps) {
  const percent = vramPercent(usedMib, totalMib);
  const color = percent >= 98 ? 'error' : percent >= 90 ? 'warning' : 'primary';
  const text = `${formatGiB(usedMib)} GB / ${formatGiB(totalMib)} GB`;
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }} data-testid="vram-bar">
      <Typography variant="caption" component="span" sx={{ minWidth: '3.5em' }}>
        VRAM
      </Typography>
      <LinearProgress
        variant="determinate"
        value={percent}
        color={color}
        aria-label={`VRAM in use: ${text}`}
        sx={{ flex: 1, height: 8, borderRadius: 4 }}
      />
      <Typography variant="caption" component="span" sx={{ whiteSpace: 'nowrap' }}>
        {text}
      </Typography>
    </Box>
  );
}
