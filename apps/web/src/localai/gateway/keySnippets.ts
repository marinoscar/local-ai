// The ready-to-paste snippets of the show-once panel (docs/specs/gateway.md Appendix A). PURE and token-free by
// construction: the functions do not take the token, so a snippet can never contain it. The key is `$LAI_KEY` in the shell
// and a file reference in OpenCode; Open WebUI is told to paste the key into its own field.

/** The gateway's base URL for this deployment: the page's own origin plus `/v1`. */
export function gatewayBaseUrl(origin: string): string {
  return `${origin.replace(/\/+$/, '')}/v1`;
}

export const KEY_FILE = '~/.config/opencode/localai-gateway-key';

export function curlSnippet(baseUrl: string): string {
  return [
    `export LAI_KEY="$(cat ${KEY_FILE})"`,
    `curl -s ${baseUrl}/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[] | {id, state: .["x-localai"].state}'`,
  ].join('\n');
}

/** Store the key in a private file: paste it, press Enter, then Ctrl+D. Nothing here holds the key. */
export function storeKeySnippet(): string {
  return `mkdir -p ~/.config/opencode\ninstall -m 600 /dev/stdin ${KEY_FILE}`;
}

interface OpenCodeModelEntry {
  name: string;
  limit?: { context: number; output: number };
}

/** The two models Appendix A.2 spells out, with the name and limits it gives them. Any other id gets a plain entry. */
const REFERENCE_MODELS: Readonly<Record<string, OpenCodeModelEntry>> = {
  'qwen3-coder-next-q4km': { name: 'Qwen3 Coder Next (lab)', limit: { context: 131072, output: 8192 } },
  'qwen3.5-9b-q4km': { name: 'Qwen 3.5 9B (lab)', limit: { context: 32768, output: 8192 } },
};

function labName(baseUrl: string): string {
  try {
    return `Local AI lab (${new URL(baseUrl).host})`;
  } catch {
    return 'Local AI lab';
  }
}

/**
 * The `localai-lab` provider block of Appendix A.2, for the given model ids (a placeholder when none are known). For the
 * lab's own origin and the two models the spec names, the output equals the spec's block exactly
 * (`docs/templates/gateway/opencode.localai-lab.json`, proved by keySnippets.test.ts).
 */
export function opencodeSnippet(baseUrl: string, models: readonly string[]): string {
  const ids = models.length > 0 ? models : ['MODEL_ID'];
  const entries = Object.fromEntries(ids.map((id) => [id, REFERENCE_MODELS[id] ?? { name: `${id} (lab)` }]));
  return JSON.stringify(
    {
      $schema: 'https://opencode.ai/config.json',
      model: `localai-lab/${ids[0]}`,
      provider: {
        'localai-lab': {
          npm: '@ai-sdk/openai-compatible',
          name: labName(baseUrl),
          options: { baseURL: baseUrl, apiKey: `{file:${KEY_FILE}}`, timeout: 600000 },
          models: entries,
        },
      },
    },
    null,
    2,
  );
}

export interface OpenWebUiField {
  label: string;
  value: string;
}

/** The fields of Open WebUI's "Add connection" form. The key is the one shown above, pasted by hand. */
export function openWebUiFields(baseUrl: string): OpenWebUiField[] {
  return [
    { label: 'URL', value: baseUrl },
    { label: 'Auth', value: 'Bearer' },
    { label: 'Key', value: 'the key shown above' },
  ];
}
