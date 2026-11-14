import { defineConfig } from 'vitest/config';

export default defineConfig({
  resolve: { preserveSymlinks: true },
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
    // @localai/ui is TypeScript source behind a node_modules link: transform it like this app's own code.
    server: { deps: { inline: ['@localai/ui'] } },
  },
});
