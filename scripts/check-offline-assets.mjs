#!/usr/bin/env node
// Offline asset scan (survival-portal.md section 4.11, PORTAL-05).
//
//   node scripts/check-offline-assets.mjs [dir]      default dir: agent/ui/dist
//
// Fails (exit 1, one line per finding: "file:line: what") when a file of the build
//   - contains an http(s):// URL that is not on the allowlist, or
//   - mentions fonts.googleapis, unpkg, jsdelivr or cdn anywhere (case-insensitive, even outside a URL), or
//   - registers a service worker (navigator.serviceWorker.register).
// Exit 0 prints "✓ offline assets: N files scanned".
//
// Allowlist: http://127.0.0.1 (any port: the agent 7070, and the Kiwix fallback address that the help text shows), http://localhost (any port), the XML namespace URIs of
// http://www.w3.org/, URLs inside licence comments (/*! ... */, @license, @preserve, Copyright) and
// LICENSE* files, and https://react.dev/errors/ (React's production error message text; it is a
// string that is shown, never fetched, and is covered by the Playwright offline test).
// Binary assets (fonts, images) are not text and are skipped.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { basename, extname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const BINARY = new Set(['.woff', '.woff2', '.ttf', '.otf', '.png', '.jpg', '.jpeg', '.gif', '.webp', '.ico', '.mp3', '.ogg', '.wav', '.mp4', '.webm', '.pmtiles', '.zim', '.gz', '.br', '.wasm']);

const ALLOWED_URL_PREFIXES = ['http://127.0.0.1:', 'http://localhost', 'http://www.w3.org/', 'https://react.dev/errors/'];
const URL_RE = /https?:\/\/[^\s"'`)<>\\]*/gi;
const FORBIDDEN_RE = /fonts\.googleapis|unpkg|jsdelivr|cdn/gi;
const SW_RE = /navigator\s*\.\s*serviceWorker\s*\.\s*register/g;

function lineOf(text, index) {
  let n = 1;
  for (let i = 0; i < index; i++) if (text.charCodeAt(i) === 10) n++;
  return n;
}

/** licenceRanges returns the [start, end) offsets of block comments that carry a licence or attribution. */
function licenceRanges(text) {
  const out = [];
  for (const m of text.matchAll(/\/\*[\s\S]*?\*\//g)) {
    const c = m[0];
    if (c.startsWith('/*!') || /@license|@preserve|copyright|licen[sc]e/i.test(c)) out.push([m.index, m.index + c.length]);
  }
  return out;
}

/** scanText returns the findings of one file's text: [{line, message}]. */
export function scanText(name, text) {
  const findings = [];
  const isLicenceFile = /^licen[sc]e/i.test(basename(name));
  const ranges = licenceRanges(text);
  const inLicence = (i) => isLicenceFile || ranges.some(([a, b]) => i >= a && i < b);

  for (const m of text.matchAll(URL_RE)) {
    const url = m[0];
    if (ALLOWED_URL_PREFIXES.some((p) => url.toLowerCase().startsWith(p.toLowerCase()))) continue;
    if (inLicence(m.index)) continue;
    findings.push({ line: lineOf(text, m.index), message: url });
  }
  for (const m of text.matchAll(FORBIDDEN_RE)) {
    findings.push({ line: lineOf(text, m.index), message: `forbidden name "${m[0]}"` });
  }
  for (const m of text.matchAll(SW_RE)) {
    findings.push({ line: lineOf(text, m.index), message: 'service worker registration' });
  }
  return findings;
}

function walk(dir) {
  const files = [];
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    const st = statSync(p);
    if (st.isDirectory()) files.push(...walk(p));
    else files.push(p);
  }
  return files;
}

/** scanDir scans every text file under dir: {files, findings: [{file, line, message}]}. */
export function scanDir(dir) {
  const findings = [];
  let files = 0;
  for (const p of walk(dir).sort()) {
    if (BINARY.has(extname(p).toLowerCase())) continue;
    files++;
    const rel = relative(dir, p);
    for (const f of scanText(rel, readFileSync(p, 'utf8'))) findings.push({ file: rel, ...f });
  }
  return { files, findings };
}

function main() {
  const dir = resolve(process.argv[2] ?? 'agent/ui/dist');
  let st;
  try {
    st = statSync(dir);
  } catch {
    console.error(`offline assets: ${dir} does not exist (run "npm run build" in agent/ui first)`);
    process.exit(2);
  }
  if (!st.isDirectory()) {
    console.error(`offline assets: ${dir} is not a directory`);
    process.exit(2);
  }
  const { files, findings } = scanDir(dir);
  if (files === 0) {
    console.error(`offline assets: no files under ${dir}: nothing was built`);
    process.exit(2);
  }
  if (findings.length > 0) {
    for (const f of findings) console.error(`${f.file}:${f.line}: ${f.message}`);
    console.error(`✗ offline assets: ${findings.length} finding(s) in ${files} files scanned`);
    process.exit(1);
  }
  console.log(`✓ offline assets: ${files} files scanned`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
