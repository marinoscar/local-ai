// Machine checks for the gateway cutover documents (GW-11, #146). Run: node --test scripts/tests/gateway-docs.test.mjs
// What is proved here, so a person does not have to eyeball it:
//   * the Open WebUI, OpenCode, curl and Python blocks of docs/specs/gateway.md Appendix A appear, unchanged, in runbook 02;
//   * the client templates parse, equal the spec, and the helper script embeds the same OpenCode provider block;
//   * nothing in these files is key-shaped, tailnet-shaped or points at the old key path;
//   * the numbered steps of the runbooks stay in order.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const read = (p) => readFileSync(join(root, p), 'utf8');

const spec = read('docs/specs/gateway.md');
const rb02 = read('docs/runbooks/02-chat-and-api-keys.md');
const rb10 = read('docs/runbooks/10-lab-operations.md');

/** Fenced code blocks of a markdown text: [{ lang, body }]. A fence may be indented (a step's code block). */
function fences(md) {
  const out = [];
  let cur = null;
  for (const line of md.split('\n')) {
    const m = /^\s*```(\w*)\s*$/.exec(line);
    if (m && cur === null) cur = { lang: m[1], lines: [] };
    else if (m && cur !== null) { out.push({ lang: cur.lang, body: cur.lines.join('\n') }); cur = null; }
    else if (cur !== null) cur.lines.push(line);
  }
  return out;
}
/** Whitespace-insensitive form: each line trimmed, blank lines dropped. */
const norm = (s) => s.split('\n').map((l) => l.trim()).filter((l) => l !== '').join('\n');
const section = (md, from, to) => {
  const a = md.indexOf(from);
  assert.ok(a >= 0, `missing ${from}`);
  const b = to ? md.indexOf(to, a + from.length) : md.length;
  assert.ok(b > a, `missing ${to}`);
  return md.slice(a, b);
};

const appendix = section(spec, '## Appendix A. Client wiring', '## 11. Open questions');
const a1 = section(appendix, '### A.1', '### A.2');
const a2 = section(appendix, '### A.2', '### A.3');
const a3 = section(appendix, '### A.3');
const rb02Fences = fences(rb02);
const rb02Bash = rb02Fences.filter((f) => f.lang === 'bash' || f.lang === '').map((f) => norm(f.body));

test('Appendix A.1: every shell block of the spec is in runbook 02, unchanged', () => {
  const blocks = fences(a1).filter((f) => f.lang === 'bash');
  assert.equal(blocks.length, 3, 'backup, database write, verify');
  for (const b of blocks) {
    assert.ok(rb02Bash.includes(norm(b.body)), `runbook 02 lacks this A.1 block:\n${b.body}`);
  }
});

test('Appendix A.1: the UI path words of the spec are in runbook 02', () => {
  for (const phrase of ['Admin Panel', 'Settings', 'Connections', 'OpenAI API', 'https://ai.marin.cr/v1', 'prefix ID empty',
    'https://ai-n1.marin.cr/v1', 'https://ai-n2.marin.cr', 'https://ai-n3.marin.cr/v1', 'qwen3.5-9b-q4km', 'OPENAI_API_BASE_URL=https://ai.marin.cr/v1']) {
    assert.ok(rb02.includes(phrase), `runbook 02 lacks: ${phrase}`);
  }
});

test('Appendix A.2: the JSON of the spec equals the runbook block and the template file', () => {
  const specJson = JSON.parse(fences(a2).find((f) => f.lang === 'json').body);
  const inRunbook = rb02Fences.filter((f) => f.lang === 'json').map((f) => JSON.parse(f.body));
  assert.ok(inRunbook.some((j) => JSON.stringify(j) === JSON.stringify(specJson)), 'runbook 02 has no block equal to A.2');
  assert.deepEqual(JSON.parse(read('docs/templates/gateway/opencode.localai-lab.json')), specJson);
});

test('Appendix A.2: the node template keeps local-llama and has the same localai-lab provider', () => {
  const specJson = JSON.parse(fences(a2).find((f) => f.lang === 'json').body);
  const node = JSON.parse(read('docs/templates/gateway/opencode.node-and-lab.json'));
  assert.deepEqual(node.provider['localai-lab'], specJson.provider['localai-lab']);
  assert.equal(node.provider['local-llama'].options.baseURL, 'http://127.0.0.1:8080/v1');
  assert.ok(node.model.startsWith('local-llama/'), 'a node defaults to the provider that works offline');
});

test('Appendix A.2: gateway-client.sh embeds the spec provider block', () => {
  const specJson = JSON.parse(fences(a2).find((f) => f.lang === 'json').body);
  const script = read('scripts/gateway-client.sh');
  const m = /<<'JSON'\n([\s\S]*?)\nJSON\n/.exec(script);
  assert.ok(m, 'provider_block heredoc not found');
  assert.deepEqual(JSON.parse(m[1]), specJson.provider['localai-lab']);
});

test('Appendix A.2/A.3: how the key is stored is the spec text, in runbook 02', () => {
  const store = 'install -m 600 /dev/stdin ~/.config/opencode/localai-gateway-key';
  assert.ok(a2.includes(store) && rb02Bash.some((b) => b.includes(store)));
});

test('Appendix A.3: every curl line of the spec is in runbook 02 and the Python client is the template', () => {
  const specBash = fences(a3).find((f) => f.lang === 'bash');
  const wanted = norm(specBash.body);
  assert.ok(rb02Bash.includes(wanted), `runbook 02 lacks the A.3 shell block:\n${specBash.body}`);
  const specPy = norm(fences(a3).find((f) => f.lang === 'python').body);
  assert.ok(rb02Fences.some((f) => f.lang === 'python' && norm(f.body) === specPy), 'runbook 02 lacks the A.3 Python block');
  assert.equal(norm(read('docs/templates/gateway/lab-client.py')), specPy);
});

test('the templates hold no key: file references and placeholders only', () => {
  const env = read('docs/templates/gateway/openwebui.env.example');
  const vars = Object.fromEntries(env.split('\n').filter((l) => /^[A-Z_]+=/.test(l)).map((l) => l.split(/=(.*)/s).slice(0, 2)));
  assert.equal(vars.OPENAI_API_BASE_URL, 'https://ai.marin.cr/v1');
  assert.match(vars.OPENAI_API_KEY, /^<[A-Z_]+>$/, 'the key must be a <PLACEHOLDER>');
  for (const f of ['opencode.localai-lab.json', 'opencode.node-and-lab.json']) {
    const j = JSON.parse(read(`docs/templates/gateway/${f}`));
    for (const [name, p] of Object.entries(j.provider)) {
      assert.match(p.options.apiKey, /^\{file:~\/\.config\/opencode\/[a-z-]+\}$/, `${f} ${name}: apiKey must be a {file:...} reference`);
    }
  }
});

const checked = [
  'docs/runbooks/02-chat-and-api-keys.md', 'docs/runbooks/05-add-a-node.md', 'docs/runbooks/07-troubleshooting.md', 'docs/runbooks/10-lab-operations.md',
  'docs/templates/gateway/README.md', 'docs/templates/gateway/opencode.localai-lab.json', 'docs/templates/gateway/opencode.node-and-lab.json',
  'docs/templates/gateway/openwebui.env.example', 'docs/templates/gateway/lab-client.py',
  'scripts/gateway-client.sh', 'scripts/openwebui-cutover.sh',
];

test('no file of the cutover contains a key, a tailnet IP or a private address', () => {
  for (const f of checked) {
    const t = read(f);
    assert.doesNotMatch(t, /lai_[A-Za-z0-9_-]{43}/, `${f}: a lai_ key`);
    assert.doesNotMatch(t, /\b(nod|pat)_[A-Za-z0-9_-]{20,}/, `${f}: a nod_/pat_ token`);
    assert.doesNotMatch(t, /\bsk-[A-Za-z0-9]{20,}/, `${f}: an sk- key`);
    assert.doesNotMatch(t, /\b[0-9a-f]{64}\b/, `${f}: a 64-hex node key`);
    assert.doesNotMatch(t, /\b100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d+\.\d+\b/, `${f}: a tailnet address`);
    assert.doesNotMatch(t, /\b(10\.\d+\.\d+\.\d+|192\.168\.\d+\.\d+|172\.(1[6-9]|2\d|3[01])\.\d+\.\d+)\b/, `${f}: a private address`);
  }
});

test('the old key path of runbook 05 is gone everywhere', () => {
  for (const f of [...checked, 'docs/specs/gateway.md']) {
    assert.ok(!read(f).includes('.config/localai/gateway-key'), `${f} still names ~/.config/localai/gateway-key`);
  }
});

test('the numbered steps of runbooks 02 and 10 count up without a gap or a repeat', () => {
  for (const [name, md] of [['02', rb02], ['10', rb10]]) {
    const nums = [...md.matchAll(/^(\d+)\. /gm)].map((m) => Number(m[1]));
    nums.forEach((n, i) => assert.equal(n, i + 1, `runbook ${name}: step ${n} is out of order (position ${i + 1})`));
  }
});
