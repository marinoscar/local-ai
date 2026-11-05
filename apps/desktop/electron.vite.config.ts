// Three builds (desktop.md section 3.1): main, preload (shell.ts, agent.ts, pin.ts) and the renderer (the shell pages).
// Dependencies are bundled into out/ (they are all devDependencies), so app.asar holds only out/** and package.json.
// The renderer must load nothing from the network: fonts come from @fontsource packages bundled by Vite, and the
// offline-asset test (tests/unit/renderer-offline.test.ts) scans the built HTML and CSS for remote URLs.
import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'electron-vite';

const pages = ['topbar', 'error', 'pin', 'connection-info', 'link', 'lab'] as const;

export default defineConfig({
  main: {
    build: {
      outDir: 'out/main',
      sourcemap: false,
      rollupOptions: {
        input: { index: resolve(__dirname, 'src/main/index.ts') },
      },
    },
  },
  preload: {
    build: {
      outDir: 'out/preload',
      sourcemap: false,
      rollupOptions: {
        input: {
          shell: resolve(__dirname, 'src/preload/shell.ts'),
          agent: resolve(__dirname, 'src/preload/agent.ts'),
          pin: resolve(__dirname, 'src/preload/pin.ts'),
        },
      },
    },
  },
  renderer: {
    root: resolve(__dirname, 'src/renderer'),
    plugins: [react()],
    // @localai/ui is a `file:` link to packages/ui, which has no node_modules: its imports (React, MUI, the icons) must resolve
    // from apps/desktop/node_modules, i.e. from the symlink's own path (the same setting as apps/cloud/apps/web).
    resolve: { preserveSymlinks: true },
    build: {
      outDir: resolve(__dirname, 'out/renderer'),
      emptyOutDir: true,
      sourcemap: false,
      // No inlined assets: a data: URL would be an extra thing for the CSP in DSK-02 to allow.
      assetsInlineLimit: 0,
      rollupOptions: {
        input: Object.fromEntries(pages.map((page) => [page, resolve(__dirname, `src/renderer/${page}/index.html`)])),
      },
    },
  },
});
