import { Box, Typography } from '@mui/material';

import { useLang } from '../lang';
import { ensureSummary, type EnsureTone } from './ensure';
import type { EnsureCard } from './types';

export interface EnsureLineProps {
  ensure: EnsureCard | null | undefined;
  /** The `data-testid` of the line (the card and the Ark tab each have their own). */
  testId?: string;
}

const COLOUR: Record<EnsureTone, string> = { success: 'success.main', info: 'info.main', warning: 'warning.main' };

/**
 * "Complete", "Installing (2 of 4)" or "Needs you: <one thing>": the first line about what a node has installed from its own
 * kit, in the viewer's language. A mark and words, never colour alone. Renders nothing for a node that has not reported
 * the state (an agent that predates it), so an old node looks exactly as it did.
 */
export function EnsureLine({ ensure, testId = 'ensure-line' }: EnsureLineProps) {
  const lang = useLang();
  if (!ensure) return null;
  const summary = ensureSummary(ensure, lang);
  return (
    <Box data-testid={testId} data-state={ensure.state} role="status" sx={{ display: 'flex', alignItems: 'baseline', gap: 0.75, color: COLOUR[summary.tone] }}>
      <Box component="span" aria-hidden="true" sx={{ fontWeight: 700 }}>
        {summary.mark}
      </Box>
      <Typography variant="body2" component="span" sx={{ color: 'text.primary', fontWeight: 600 }}>
        {summary.text}
      </Typography>
    </Box>
  );
}
