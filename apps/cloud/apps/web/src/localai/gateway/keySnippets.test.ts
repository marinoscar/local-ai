import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { FIXTURE_TOKEN } from './fixture';
import { curlSnippet, gatewayBaseUrl, KEY_FILE, opencodeSnippet, openWebUiFields, storeKeySnippet } from './keySnippets';

const BASE = 'https://ai.marin.cr/v1';
const ROOT = join(__dirname, '../../../../../../..');
const readRoot = (path: string): string => readFileSync(join(ROOT, path), 'utf8');

describe('the snippets', () => {
  it('build the base URL from the page origin', () => {
    expect(gatewayBaseUrl('https://ai.marin.cr')).toBe(BASE);
    expect(gatewayBaseUrl('http://localhost:3535/')).toBe('http://localhost:3535/v1');
  });

  it('curl reads the key from $LAI_KEY and lists the models', () => {
    const curl = curlSnippet(BASE);
    expect(curl).toContain(`curl -s ${BASE}/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[] | {id, state: .["x-localai"].state}'`);
    expect(curl).toContain(`$(cat ${KEY_FILE})`);
  });

  it('the key is stored with install -m 600 from stdin', () => {
    expect(storeKeySnippet()).toContain(`install -m 600 /dev/stdin ${KEY_FILE}`);
  });

  it('OpenCode is the localai-lab provider block of Appendix A.2, key by file reference', () => {
    const config = JSON.parse(opencodeSnippet(BASE, ['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km']));
    expect(config.model).toBe('localai-lab/qwen3-coder-next-q4km');
    expect(config.provider['localai-lab']).toMatchObject({
      npm: '@ai-sdk/openai-compatible',
      options: { baseURL: BASE, apiKey: `{file:${KEY_FILE}}` },
    });
    expect(Object.keys(config.provider['localai-lab'].models)).toEqual(['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km']);
  });

  it('OpenCode shows a placeholder model when the key allows every model', () => {
    expect(Object.keys(JSON.parse(opencodeSnippet(BASE, [])).provider['localai-lab'].models)).toEqual(['MODEL_ID']);
  });

  it('Open WebUI gets the URL, Bearer auth and a pointer to the key', () => {
    expect(openWebUiFields(BASE)).toEqual([
      { label: 'URL', value: BASE },
      { label: 'Auth', value: 'Bearer' },
      { label: 'Key', value: 'the key shown above' },
    ]);
  });

  it('no snippet can contain a token: none takes one, and none has a key-shaped string', () => {
    const all = [curlSnippet(BASE), storeKeySnippet(), opencodeSnippet(BASE, []), JSON.stringify(openWebUiFields(BASE))].join('\n');
    expect(all).not.toContain(FIXTURE_TOKEN);
    expect(all).not.toMatch(/lai_[A-Za-z0-9_-]{43}/);
  });

  // GW-11 (#146): the dialog and docs/specs/gateway.md Appendix A must not drift. The spec and the template are read from
  // the repository, so editing either side without the other fails here.
  describe('equals Appendix A of the spec', () => {
    const spec = readRoot('docs/specs/gateway.md');
    const appendix = spec.slice(spec.indexOf('## Appendix A. Client wiring'));

    it('OpenCode: the block for the two models of A.2 is the spec block, byte for byte after parsing', () => {
      const specJson = /### A\.2 OpenCode[\s\S]*?```json\n([\s\S]*?)\n```/.exec(appendix)?.[1];
      expect(specJson).toBeDefined();
      const fromDialog = JSON.parse(opencodeSnippet(BASE, ['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km']));
      expect(fromDialog).toEqual(JSON.parse(specJson as string));
    });

    it('OpenCode: the spec block equals the template file the runbook points to', () => {
      const specJson = /### A\.2 OpenCode[\s\S]*?```json\n([\s\S]*?)\n```/.exec(appendix)?.[1] as string;
      expect(JSON.parse(readRoot('docs/templates/gateway/opencode.localai-lab.json'))).toEqual(JSON.parse(specJson));
    });

    it('curl: the dialog lines are lines of the spec A.3 block, and the key file is the one A.2 and A.3 name', () => {
      const a3 = /### A\.3 curl and Python[\s\S]*?```bash\n([\s\S]*?)\n```/.exec(appendix)?.[1] as string;
      for (const line of curlSnippet(BASE).split('\n')) expect(a3).toContain(line);
      expect(a3).toContain(KEY_FILE);
      expect(storeKeySnippet()).toContain(`install -m 600 /dev/stdin ${KEY_FILE}`);
      expect(appendix).toContain('install -m 600 /dev/stdin ~/.config/opencode/localai-gateway-key');
    });

    it('the key file path is the same in the dialog and in every runbook (no ~/.config/localai/gateway-key)', () => {
      for (const file of ['docs/runbooks/02-chat-and-api-keys.md', 'docs/runbooks/05-add-a-node.md', 'docs/runbooks/10-lab-operations.md']) {
        expect(readRoot(file)).not.toContain('~/.config/localai/gateway-key');
      }
    });
  });
});
