// A monospace block that scrolls sideways instead of widening the page (390 px phones), focusable so a keyboard user can
// scroll it. No syntax highlighter and no web font: nothing is fetched at run time (offline-first).
import { Box } from '@mui/material';

export function CodeBlock({ children, label }: { children: string; label: string }) {
  return (
    <Box
      component="pre"
      tabIndex={0}
      aria-label={label}
      sx={{
        m: 0,
        p: 1.5,
        overflowX: 'auto',
        maxWidth: '100%',
        bgcolor: 'action.hover',
        border: 1,
        borderColor: 'divider',
        borderRadius: 1,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        fontSize: '0.8125rem',
        lineHeight: 1.5,
        whiteSpace: 'pre',
      }}
    >
      {children}
    </Box>
  );
}
