import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import QuestionMarkIcon from '@mui/icons-material/QuestionMark';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import { Chip } from '@mui/material';
import type { ReactElement } from 'react';

import { useLang } from '../lang';
import type { ArkColour } from './types';

export interface ArkChipProps {
  colour: ArkColour;
  /** The API's words for the reason ("ready", "tier 2 partial", "41 days", "Drive not plugged in · last synced 6 days ago"). */
  label: string;
  /** What the chip is about, for the accessible name: "internal" or "drive". */
  subject?: string;
  size?: 'small' | 'medium';
}

const STATUS: Record<ArkColour, { word: string; wordEs: string; color: 'success' | 'warning' | 'error' | 'default'; icon: ReactElement }> = {
  green: { word: 'ready', wordEs: 'listo', color: 'success', icon: <CheckCircleIcon /> },
  amber: { word: 'needs attention', wordEs: 'requiere atención', color: 'warning', icon: <WarningAmberIcon /> },
  red: { word: 'not ready', wordEs: 'no está listo', color: 'error', icon: <ErrorIcon /> },
  grey: { word: 'no data', wordEs: 'sin datos', color: 'default', icon: <QuestionMarkIcon /> },
};

/** The ark readiness of one root: colour + the API's label + icon. The accessible name adds the status word. */
export function ArkChip({ colour, label, subject, size = 'small' }: ArkChipProps) {
  const { word: wordEn, wordEs, color, icon } = STATUS[colour];
  const word = useLang() === 'es' ? wordEs : wordEn;
  const name = `${subject ? `${subject}: ` : ''}${word}, ${label}`;
  return (
    <Chip
      size={size}
      color={color}
      variant={colour === 'grey' ? 'outlined' : 'filled'}
      icon={icon}
      label={label}
      aria-label={name}
      data-testid="ark-chip"
      data-colour={colour}
      // A long label ("Drive not plugged in · last synced 6 days ago") wraps instead of being cut off.
      sx={{ height: 'auto', maxWidth: '100%', '& .MuiChip-label': { whiteSpace: 'normal', py: 0.25 } }}
    />
  );
}
