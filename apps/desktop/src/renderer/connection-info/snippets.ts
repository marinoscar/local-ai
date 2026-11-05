// The ready snippets of the connection-info page (docs/specs/desktop.md 4.6). Pure functions: the page and its unit test
// import them. The agent's snippets carry the placeholders `$KEY` and `<key>`; the key replaces them only after an
// explicit Reveal. Before that, the text stays exactly as the agent sent it.
import type { KeyFailure } from '../../shared/types';

/** The file the OpenCode provider block reads the key from (`{file:...}`), and the command that writes it. */
export const OPENCODE_KEY_FILE = '~/.config/opencode/llama-api-key';
export const OPENCODE_WRITE_COMMAND = `install -m 600 /dev/stdin ${OPENCODE_KEY_FILE}`;

const PLACEHOLDER = /\$KEY\b|<key>/g;

/** Replaces `$KEY` and `<key>` with `key`; with no key (not revealed) the text is returned unchanged. */
export function renderSnippet(text: string, key: string | null): string {
  if (key === null || key === '') return text;
  // A function, so a `$` in the key could never be read as a replacement pattern.
  return text.replace(PLACEHOLDER, () => key);
}

/** The curl line that needs no key in the command: it reads the file the OpenCode tab writes. */
export function curlModelsCommand(localUrl: string): string {
  return `curl ${localUrl}/models -H "Authorization: Bearer $(cat ${OPENCODE_KEY_FILE})"`;
}

/** What the page shows for an agent or desktop refusal: the text, the doctor code (when it is one) and the fix. */
export function describeFailure(failure: KeyFailure, fallback: { text: string; fix: string }): { text: string; code?: string; fix: string } {
  return {
    text: failure.message ?? fallback.text,
    ...(failure.code.startsWith('LAI-') ? { code: failure.code } : {}),
    fix: failure.fix ?? fallback.fix,
  };
}
