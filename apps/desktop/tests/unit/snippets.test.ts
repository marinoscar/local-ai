// The connection-info page's snippets (docs/specs/desktop.md 4.6): the key replaces the placeholders only after Reveal.
import { randomBytes } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { buildConnectionInfo, maskKey, parseKeyResponse } from '../../src/main/clipboard';
import { OPENCODE_KEY_FILE, OPENCODE_WRITE_COMMAND, curlModelsCommand, describeFailure, renderSnippet } from '../../src/renderer/connection-info/snippets';
import { fill } from '../../src/renderer/connection-info/strings';

const newKey = (): string => randomBytes(32).toString('hex');

describe('renderSnippet', () => {
  const curl = 'curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $KEY"';
  const env = 'OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=<key>';

  it('before Reveal (no key) the text is exactly what the agent sent', () => {
    expect(renderSnippet(curl, null)).toBe(curl);
    expect(renderSnippet(env, null)).toBe(env);
    expect(renderSnippet(env, '')).toBe(env);
  });

  it('after Reveal it replaces $KEY and <key>, everywhere they occur', () => {
    const key = newKey();
    expect(renderSnippet(curl, key)).toBe(`curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer ${key}"`);
    expect(renderSnippet(env, key)).toBe(`OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=${key}`);
    expect(renderSnippet('<key> and <key> and $KEY', key)).toBe(`${key} and ${key} and ${key}`);
  });

  it('leaves similar text alone ($KEYS, $KEY_FILE)', () => {
    const key = newKey();
    expect(renderSnippet('$KEYS $KEY_FILE', key)).toBe('$KEYS $KEY_FILE');
  });

  it('a key with a dollar-pattern is inserted literally', () => {
    expect(renderSnippet('a $KEY b', "$&$'")).toBe("a $&$' b");
  });
});

describe('what the page gets before Reveal', () => {
  it('has a masked key and placeholder snippets, and no way to rebuild the key', () => {
    const key = newKey();
    const info = buildConnectionInfo(
      parseKeyResponse({ key, models: ['model-a'], snippets: { curl: 'curl -H "Authorization: Bearer $KEY"', env: 'OPENAI_API_KEY=<key>' } })!,
    );
    expect(info.keyMasked).toBe(maskKey(key));
    expect(info.keyMasked).toMatch(/^[0-9a-f]{4}…[0-9a-f]{4}$/);
    const page = [info.keyMasked, ...Object.values(info.snippets)].join('\n');
    expect(page).not.toContain(key);
    // Revealing is the only step that puts the key into the text.
    expect(renderSnippet(info.snippets.curl, null)).not.toContain(key);
    expect(renderSnippet(info.snippets.curl, key)).toContain(key);
  });
});

describe('the OpenCode and curl commands', () => {
  it('write the key file with a mode only the user can read, from stdin, never from the command line', () => {
    expect(OPENCODE_WRITE_COMMAND).toBe('install -m 600 /dev/stdin ~/.config/opencode/llama-api-key');
    expect(OPENCODE_KEY_FILE).toBe('~/.config/opencode/llama-api-key');
  });
  it('the curl line reads the key from that file and uses the local URL', () => {
    expect(curlModelsCommand('http://127.0.0.1:8080/v1')).toBe('curl http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $(cat ~/.config/opencode/llama-api-key)"');
  });
});

describe('describeFailure and fill', () => {
  it('shows the agent’s message and fix and the code when it is a doctor code', () => {
    expect(describeFailure({ ok: false, code: 'LAI-SEC-010', message: 'damaged', fix: 'Run sudo aictl key rotate' }, { text: 'x', fix: 'y' })).toEqual({ text: 'damaged', code: 'LAI-SEC-010', fix: 'Run sudo aictl key rotate' });
    expect(describeFailure({ ok: false, code: 'clipboard_failed' }, { text: 'x', fix: 'y' })).toEqual({ text: 'x', fix: 'y' });
  });
  it('fill replaces named placeholders and leaves unknown ones', () => {
    expect(fill('in {n} seconds {z}', { n: 60 })).toBe('in 60 seconds {z}');
  });
});
