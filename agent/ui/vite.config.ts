import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

// `emptyOutDir` removes dist/.gitkeep, the placeholder that keeps `go:embed all:dist` compiling on a fresh
// clone (agent/ui/embed.go). A build must leave it behind or the next commit deletes it (happened in #353).
function keepGitkeep(): Plugin {
  return {
    name: 'keep-gitkeep',
    closeBundle() {
      mkdirSync(resolve(__dirname, 'dist'), { recursive: true });
      writeFileSync(resolve(__dirname, 'dist', '.gitkeep'), '');
    },
  };
}

// The Portal is served by the agent under /portal (survival-portal.md section 3.1), so every asset
// URL in portal.html starts with /portal/assets/. Nothing is loaded from another origin (section
// 4.11): fonts are emitted as files (never data: URIs, the CSP font-src is 'self') and the build has
// no inline script or style.
export default defineConfig({
  base: '/portal/',
  plugins: [react(), keepGitkeep()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    cssCodeSplit: false,
    sourcemap: false,
    rollupOptions: {
      input: {
        portal: resolve(__dirname, 'portal.html'),
        index: resolve(__dirname, 'index.html'),
      },
    },
  },
  server: {
    proxy: { '/api': 'http://127.0.0.1:7070' },
  },
});
