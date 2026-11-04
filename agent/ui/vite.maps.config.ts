import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

// The offline map viewer, served by the agent at /maps/ (companion-services.md section 4.4.3). It is
// a second build into the same dist/ (run after the Portal build, which empties dist/): maps.html at
// the top and its bundle in dist/viewer/, because /maps/assets/ belongs to the map fonts and sprites
// (SVC-09). Nothing is loaded from another origin; files are emitted, never inlined as data: URIs.
// Libraries carry a few http(s) strings that are shown or linked, never fetched (the MapLibre logo
// link, error messages that point at docs). They would be dead links offline and the offline asset
// scan (scripts/check-offline-assets.mjs) rejects any URL, so they are rewritten at build time.
const DEAD_LINKS: [string, string][] = [
  ['https://maplibre.org/', '#'],
  ['See https://github.com/mapbox/mapbox-gl-js/issues/2907', ''],
  [' See https://github.com/protomaps/PMTiles/tree/main/js for details.', ''],
];
function stripDeadLinks(): Plugin {
  return {
    name: 'strip-dead-links',
    renderChunk(code) {
      let out = code;
      for (const [from, to] of DEAD_LINKS) out = out.split(from).join(to);
      return { code: out, map: null };
    },
  };
}

export default defineConfig({
  base: '/maps/',
  plugins: [react(), stripDeadLinks()],
  build: {
    outDir: 'dist',
    emptyOutDir: false,
    assetsDir: 'viewer',
    assetsInlineLimit: 0,
    cssCodeSplit: false,
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
    rollupOptions: {
      input: { maps: resolve(__dirname, 'maps.html') },
    },
  },
  server: {
    proxy: { '/maps': 'http://127.0.0.1:7070' },
  },
});
