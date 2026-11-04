// Shared bootstrap of every shell page (React 19 + MUI). The font is bundled from @fontsource, never fetched
// (docs/specs/desktop.md section 3.1: no network assets).
import '@fontsource/atkinson-hyperlegible/400.css';
import '@fontsource/atkinson-hyperlegible/700.css';
import { CssBaseline, ThemeProvider, Typography, createTheme } from '@mui/material';
import type { ReactElement } from 'react';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

const theme = createTheme({
  typography: { fontFamily: '"Atkinson Hyperlegible", system-ui, sans-serif', button: { textTransform: 'none' } },
});

/** Mounts a shell page into #root. */
export function mountPage(page: ReactElement): void {
  const root = document.getElementById('root');
  if (!root) throw new Error('shell page has no #root element');
  createRoot(root).render(
    <StrictMode>
      <ThemeProvider theme={theme}>
        <CssBaseline />
        {page}
      </ThemeProvider>
    </StrictMode>,
  );
}

/** The DSK-01 placeholder body; each page is replaced by its own item. */
export function Placeholder({ title, owner }: { title: string; owner: string }): ReactElement {
  return (
    <Typography component="main" sx={{ p: 3 }}>
      <strong>{title}</strong> ({owner})
    </Typography>
  );
}
