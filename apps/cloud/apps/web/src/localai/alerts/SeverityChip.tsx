import ErrorIcon from '@mui/icons-material/Error';
import InfoOutlinedIcon from '@mui/icons-material/InfoOutlined';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import { Chip } from '@mui/material';

import type { AlertSeverity } from './types';

/** The word beside the icon: colour is never the only signal. */
export const SEVERITY_WORD: Record<AlertSeverity, string> = { critical: 'critical', warning: 'warning', info: 'info' };

/** Severity as icon + word + colour (red / amber / blue). */
export function SeverityChip({ severity }: { severity: AlertSeverity }) {
  const props = {
    critical: { color: 'error', icon: <ErrorIcon /> },
    warning: { color: 'warning', icon: <WarningAmberIcon /> },
    info: { color: 'info', icon: <InfoOutlinedIcon /> },
  } as const;
  const { color, icon } = props[severity];
  return <Chip size="small" color={color} icon={icon} label={SEVERITY_WORD[severity]} data-testid="severity-chip" data-severity={severity} sx={{ flexShrink: 0 }} />;
}
