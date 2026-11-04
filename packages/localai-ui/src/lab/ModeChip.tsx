import QuestionMarkIcon from '@mui/icons-material/QuestionMark';
import HealthAndSafetyIcon from '@mui/icons-material/HealthAndSafety';
import PowerIcon from '@mui/icons-material/Power';
import PowerOffIcon from '@mui/icons-material/PowerOff';
import { Chip } from '@mui/material';

import type { LabMode } from './types';

export interface ModeChipProps {
  mode: LabMode | null;
  size?: 'small' | 'medium';
}

/**
 * online = green, offline = amber, survival = the theme's secondary colour (purple-red in the Local AI theme) with the
 * word "Survival", unknown = grey. Colour + word + icon.
 */
export function ModeChip({ mode, size = 'small' }: ModeChipProps) {
  switch (mode) {
    case 'online':
      return <Chip size={size} color="success" variant="outlined" icon={<PowerIcon />} label="Online" data-testid="mode-chip" data-mode="online" />;
    case 'offline':
      return <Chip size={size} color="warning" variant="outlined" icon={<PowerOffIcon />} label="Offline" data-testid="mode-chip" data-mode="offline" />;
    case 'survival':
      return <Chip size={size} color="secondary" variant="filled" icon={<HealthAndSafetyIcon />} label="Survival" data-testid="mode-chip" data-mode="survival" />;
    default:
      return <Chip size={size} variant="outlined" icon={<QuestionMarkIcon />} label="Unknown" data-testid="mode-chip" data-mode="unknown" />;
  }
}
