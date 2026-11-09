import QuestionMarkIcon from '@mui/icons-material/QuestionMark';
import HealthAndSafetyIcon from '@mui/icons-material/HealthAndSafety';
import PowerIcon from '@mui/icons-material/Power';
import PowerOffIcon from '@mui/icons-material/PowerOff';
import { Chip } from '@mui/material';

import { useLang } from '../lang';
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
  const es = useLang() === 'es';
  switch (mode) {
    case 'online':
      return <Chip size={size} color="success" variant="outlined" icon={<PowerIcon />} label={es ? 'En línea' : 'Online'} data-testid="mode-chip" data-mode="online" />;
    case 'offline':
      return <Chip size={size} color="warning" variant="outlined" icon={<PowerOffIcon />} label={es ? 'Sin conexión' : 'Offline'} data-testid="mode-chip" data-mode="offline" />;
    case 'survival':
      return <Chip size={size} color="secondary" variant="filled" icon={<HealthAndSafetyIcon />} label={es ? 'Supervivencia' : 'Survival'} data-testid="mode-chip" data-mode="survival" />;
    default:
      return <Chip size={size} variant="outlined" icon={<QuestionMarkIcon />} label={es ? 'Desconocido' : 'Unknown'} data-testid="mode-chip" data-mode="unknown" />;
  }
}
