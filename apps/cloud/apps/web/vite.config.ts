/// <reference types="vitest/config" />
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import react from '@vitejs/plugin-react';
import { build as viteBuild, defineConfig, type Plugin } from 'vite';

/** The identity fields the page shell and the manifest show (packages/shared/identity.json). */
export interface WebIdentity {
  productName: string;
  themeColor: string;
  backgroundColor: string;
}

// `import.meta.dirname` when Vite loads this file; the workspace directory under a jsdom test.
const HERE = typeof import.meta.dirname === 'string' ? import.meta.dirname : process.cwd();
const IDENTITY_PATH = join(HERE, '..', '..', 'packages', 'shared', 'identity.json');

export function readIdentity(path = IDENTITY_PATH): WebIdentity {
  return JSON.parse(readFileSync(path, 'utf8')) as WebIdentity;
}

const SLICES_PATH = join(HERE, '..', '..', 'packages', 'shared', 'slices.json');

/** The slice ids in `packages/shared/slices.json` `enabled` (validated at run time by `@app/shared`). */
export function readEnabledSlices(path = SLICES_PATH): string[] {
  return (JSON.parse(readFileSync(path, 'utf8')) as { enabled: string[] }).enabled;
}

/**
 * Bundles `src/sw.ts` (the Web Push service worker) into one classic script:
 * no imports at run time, so it can be served as /sw.js with no build graph
 * around it. A separate build on purpose: the worker must not share chunks
 * with the app (a worker cannot load React).
 */
export async function buildServiceWorker(entry = join(HERE, 'src', 'sw.ts')): Promise<string> {
  const result = (await viteBuild({
    configFile: false,
    logLevel: 'silent',
    build: { write: false, minify: true, lib: { entry, formats: ['iife'], name: 'appServiceWorker', fileName: () => 'sw.js' } },
  })) as unknown as { output: Array<{ type: string; code?: string }> } | Array<{ output: Array<{ type: string; code?: string }> }>;
  const chunk = (Array.isArray(result) ? result[0]! : result).output.find((item) => item.type === 'chunk');
  if (chunk?.code === undefined) throw new Error('the service worker build produced no script');
  return chunk.code;
}

/**
 * Serves (dev) and emits (build) /sw.js while the notifications slice is
 * enabled; with the slice off there is no worker and /sw.js is a 404.
 */
function serviceWorkerPlugin(): Plugin {
  const enabled = () => readEnabledSlices().includes('notifications');
  return {
    name: 'app-service-worker',
    configureServer(server) {
      server.middlewares.use('/sw.js', (_req, res, next) => {
        if (!enabled()) return next();
        buildServiceWorker().then(
          (code) => {
            res.setHeader('Content-Type', 'text/javascript');
            res.setHeader('Cache-Control', 'no-cache');
            res.end(code);
          },
          next,
        );
      });
    },
    async generateBundle() {
      if (enabled()) this.emitFile({ type: 'asset', fileName: 'sw.js', source: await buildServiceWorker() });
    },
  };
}

/** `index.html` with the identity in its `<title>` and theme colour. */
export function renderIndexHtml(html: string, identity: WebIdentity): string {
  const escape = (value: string) => value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
  return html.replaceAll('%APP_NAME%', escape(identity.productName)).replaceAll('%APP_THEME_COLOR%', identity.themeColor);
}

/** The web app manifest, from the same identity. */
export function renderManifest(identity: WebIdentity): string {
  const manifest = {
    name: identity.productName,
    short_name: identity.productName,
    start_url: '/',
    display: 'standalone',
    theme_color: identity.themeColor,
    background_color: identity.backgroundColor,
    icons: [{ src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml' }],
  };
  return `${JSON.stringify(manifest, null, 2)}\n`;
}

/** Renders the shell and serves/emits the manifest, so a rename touches identity.json only. */
function identityPlugin(): Plugin {
  return {
    name: 'app-identity',
    transformIndexHtml: (html) => renderIndexHtml(html, readIdentity()),
    configureServer(server) {
      server.middlewares.use('/manifest.webmanifest', (_req, res) => {
        res.setHeader('Content-Type', 'application/manifest+json');
        res.end(renderManifest(readIdentity()));
      });
    },
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: 'manifest.webmanifest', source: renderManifest(readIdentity()) });
    },
  };
}

export default defineConfig({
  plugins: [react(), identityPlugin(), serviceWorkerPlugin()],
  // The packaged pages are one chunk until the app splits its routes.
  build: { chunkSizeWarningLimit: 2000 },
  server: {
    host: true,
    port: 5173,
    proxy: { '/api': { target: process.env.VITE_API_PROXY_TARGET || 'http://localhost:3000', changeOrigin: false } },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/__tests__/setup.ts'],
  },
});
