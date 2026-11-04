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
    `curl -s ${baseUrl}/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[].id'`,
  ].join('\n');
}

/** Store the key in a private file: paste it, press Enter, then Ctrl+D. Nothing here holds the key. */
export function storeKeySnippet(): string {
  return `mkdir -p ~/.config/opencode\ninstall -m 600 /dev/stdin ${KEY_FILE}`;
}

/** The `localai-lab` provider block of Appendix A.2, for the given model ids (a placeholder when none are known). */
export function opencodeSnippet(baseUrl: string, models: readonly string[]): string {
  const ids = models.length > 0 ? models : ['MODEL_ID'];
  const entries = Object.fromEntries(ids.map((id) => [id, { name: `${id} (lab)` }]));
  return JSON.stringify(
    {
      $schema: 'https://opencode.ai/config.json',
      model: `localai-lab/${ids[0]}`,
      provider: {
        'localai-lab': {
          npm: '@ai-sdk/openai-compatible',
          name: 'Local AI lab',
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
