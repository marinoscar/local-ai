import '@testing-library/jest-dom/vitest';
import { configure } from '@testing-library/react';

// findBy*/waitFor poll for 1 s by default; a page that renders a MUI table or dialog can take longer than that on a
// loaded CI runner (#377). 5 s is far above the idle time and far below the 30 s test limit of vite.config.ts.
configure({ asyncUtilTimeout: 5000 });
