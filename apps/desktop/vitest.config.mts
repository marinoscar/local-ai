import { resolve } from 'node:path';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  resolve: {
    // Under Node the `electron` package resolves to the path of the binary (a string), not the API. Unit tests get a
    // stub instead, so a main-process module can be imported without starting Electron.
    alias: { electron: resolve(import.meta.dirname, 'tests/fixtures/electron-stub.ts') },
  },
  test: {
    include: ['tests/unit/**/*.test.ts'],
    environment: 'node',
  },
});
