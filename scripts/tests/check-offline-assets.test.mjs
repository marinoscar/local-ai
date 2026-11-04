// Unit tests of scripts/check-offline-assets.mjs. Run: node --test scripts/tests/check-offline-assets.test.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { scanDir, scanText } from '../check-offline-assets.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, '..', 'check-offline-assets.mjs');
const messages = (name, text) => scanText(name, text).map((f) => f.message);

test('the SVG and XLink namespaces are allowed', () => {
  assert.deepEqual(messages('a.js', 'el("http://www.w3.org/2000/svg"); x("http://www.w3.org/1999/xlink")'), []);
});

test('loopback URLs are allowed', () => {
  assert.deepEqual(messages('a.js', 'fetch("http://127.0.0.1:7070/api"); u="http://localhost:8080/x"'), []);
});

test('another host fails with the URL and the line', () => {
  const f = scanText('a.js', 'ok\nfetch("https://example.org/x.js")\n');
  assert.equal(f.length, 1);
  assert.equal(f[0].line, 2);
  assert.equal(f[0].message, 'https://example.org/x.js');
});

test('a URL inside a licence comment is allowed', () => {
  assert.deepEqual(messages('a.js', '/*! Lib v1 | MIT | https://example.org/lib/license */\nvar a=1;'), []);
  assert.deepEqual(messages('a.js', '/** @license see https://example.org/l */\nvar a=1;'), []);
  assert.deepEqual(messages('LICENSE.txt', 'see https://www.apache.org/licenses/LICENSE-2.0'), []);
});

test('the same URL outside a licence comment fails', () => {
  assert.equal(scanText('a.js', '/* a note */ var u = "https://example.org/x";').length, 1);
});

test('cdn, unpkg, jsdelivr and fonts.googleapis fail even in a comment or outside a URL', () => {
  for (const word of ['cdn', 'CDN', 'unpkg', 'jsdelivr', 'fonts.googleapis']) {
    assert.ok(scanText('a.js', `// loaded from the ${word} later`).length >= 1, word);
  }
});

test('a service worker registration fails', () => {
  assert.deepEqual(messages('a.js', 'navigator.serviceWorker.register("/sw.js")'), ['service worker registration']);
});

test('React error decoder text is allowed', () => {
  assert.deepEqual(messages('a.js', 'var u="https://react.dev/errors/"+e'), []);
});

test('scanDir skips binary fonts and counts text files; the CLI exit codes', () => {
  const dir = mkdtempSync(join(tmpdir(), 'offline-'));
  try {
    mkdirSync(join(dir, 'assets'));
    writeFileSync(join(dir, 'portal.html'), '<!doctype html><title>x</title>');
    writeFileSync(join(dir, 'assets', 'a.woff2'), Buffer.from('cdn unpkg https://example.org'));
    assert.equal(scanDir(dir).files, 1);
    const ok = spawnSync('node', [script, dir], { encoding: 'utf8' });
    assert.equal(ok.status, 0, ok.stderr);
    assert.match(ok.stdout, /✓ offline assets: 1 files scanned/);
    writeFileSync(join(dir, 'assets', 'bad.js'), 'import "https://unpkg.com/x.js"');
    const bad = spawnSync('node', [script, dir], { encoding: 'utf8' });
    assert.equal(bad.status, 1);
    assert.match(bad.stderr, /assets\/bad\.js:1: https:\/\/unpkg\.com\/x\.js/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('an external tile URL in the maps viewer bundle (viewer/) fails like any other file', () => {
  const dir = mkdtempSync(join(tmpdir(), 'oa-maps-'));
  try {
    mkdirSync(join(dir, 'viewer'));
    writeFileSync(join(dir, 'viewer', 'maps-abc123.js'), 'const s={tiles:["https://tile.example.org/{z}/{x}/{y}.png"]};');
    const { files, findings } = scanDir(dir);
    assert.equal(files, 1);
    assert.equal(findings.length, 1);
    assert.equal(findings[0].file, join('viewer', 'maps-abc123.js'));
    assert.match(findings[0].message, /^https:\/\/tile\.example\.org\//);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('the committed negative fixture is rejected', () => {
  const r = spawnSync('node', [script, join(here, '..', 'testdata', 'offline-assets', 'bad')], { encoding: 'utf8' });
  assert.equal(r.status, 1);
  assert.match(r.stderr, /app\.js:2/);
  assert.match(r.stderr, /fonts\.googleapis/);
  assert.match(r.stderr, /example\.org/);
});
