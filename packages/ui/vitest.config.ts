import { defineConfig } from 'vitest/config';

// Component tests of the MUI-free parts (src/music, src/workloads, src/common) with fake transports. The lab
// components (MUI) are tested by their hosts.
export default defineConfig({
  esbuild: { jsx: 'automatic' },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['./src/test/setup.ts'],
  },
});
