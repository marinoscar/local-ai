import { ShellRoot } from '@marinoscar/platform-web/shell/ui';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';

import App from './App';
import { setupSlices } from './slices/manifest';
import { APP_THEMES } from './theme';

// Each enabled optional slice configures itself once, before the first render
// (the notification client, the link renderers).
setupSlices();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {/* The platform shell's theme root: the light / dark / system choice, MUI's theme and the baseline. */}
    <ShellRoot themes={APP_THEMES}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ShellRoot>
  </StrictMode>,
);
