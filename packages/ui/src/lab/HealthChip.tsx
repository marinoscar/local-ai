import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import { Chip } from '@mui/material';

import type { LabHealth, LabMode, LabPhase } from './types';

export interface HealthChipProps {
  /** The API's derived health (docs/specs/cloud-app.md section 4.5). */
  health: LabHealth;
  /** Only read for `quiet`, to say why the node is quiet. */
  mode?: LabMode | null;
  phase?: LabPhase;
  size?: 'small' | 'medium';
}

/** The words next to the icon. `quiet` names its reason: expected silence is not a fault. */
export function healthWord(health: LabHealth, mode: LabMode | null | undefined, phase: LabPhase | undefined): string {
  switch (health) {
    case 'healthy':
      return 'Healthy';
    case 'degraded':
      return 'Degraded';
    case 'down':
      return 'Down';
    case 'quiet':
      if (phase === 'pending') return 'Not connected yet';
      return mode === 'survival' ? 'Survival mode (expected)' : 'Offline mode (expected)';
  }
}

/** Health as colour + word + icon (green / amber / red / grey): colour is never the only signal. */
export function HealthChip({ health, mode, phase, size = 'small' }: HealthChipProps) {
  const label = healthWord(health, mode, phase);
  const props = {
    healthy: { color: 'success', icon: <CheckCircleIcon /> },
    degraded: { color: 'warning', icon: <WarningAmberIcon /> },
    down: { color: 'error', icon: <ErrorIcon /> },
    quiet: { color: 'default', icon: <RadioButtonUncheckedIcon /> },
  } as const;
  const { color, icon } = props[health];
  return <Chip size={size} variant={health === 'quiet' ? 'outlined' : 'filled'} color={color} icon={icon} label={label} data-testid="health-chip" data-health={health} />;
}
