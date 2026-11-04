import { FIXTURE_TOKEN } from './fixture';
import { curlSnippet, gatewayBaseUrl, KEY_FILE, opencodeSnippet, openWebUiFields, storeKeySnippet } from './keySnippets';

const BASE = 'https://ai.marin.cr/v1';

describe('the snippets', () => {
  it('build the base URL from the page origin', () => {
    expect(gatewayBaseUrl('https://ai.marin.cr')).toBe(BASE);
    expect(gatewayBaseUrl('http://localhost:3535/')).toBe('http://localhost:3535/v1');
  });

  it('curl reads the key from $LAI_KEY and lists the models', () => {
    const curl = curlSnippet(BASE);
    expect(curl).toContain(`curl -s ${BASE}/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[].id'`);
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
});
