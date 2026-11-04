// The plain-language line of a failed or expired command, for the `localai.command.failed` notification (spec 4.9:
// "{type} on {node} failed: {fix}"). The fix of a code is the one in docs/specs/doctor-codes.md; this table holds the
// codes the CLOUD itself stores in `localai_commands.error` plus the two agent codes spec section 5 names for
// `model.load`. Any other agent code falls back to the agent's own message (already scrubbed and plain language), and
// a command with neither gets a generic pointer to the Doctor. Never a stack, a claim token or a result.
import type { CommandError } from './command-failure';

const FIX_BY_CODE: Readonly<Record<string, string>> = {
  'LAI-CLD-012': "Read the code in the command's error for the exact fix, then send it again",
  'LAI-CLD-015': 'Nothing is broken. Tell the admin; the update will be retried later',
  'LAI-CLD-030': "Check that the cloud's background jobs run (Admin → Jobs)",
  'LAI-CLD-057': 'Nothing to do. To allow it, edit `cloud.allow` in `node.yaml`',
  'LAI-CLD-058': 'Report it to the maintainer',
  'LAI-CLD-059': 'Nothing to do; the cloud shows the current state',
  'LAI-CAT-005': 'Choose a smaller model. `aictl models --loadable` lists the ones that fit',
  'LAI-LLM-011': 'Unload a model first, or allow unloading others',
};

const GENERIC_FIX = "See the command's details on the node's page";

/** The one-sentence fix shown for `error` (the stored `{ code, message }`, or null when none was stored). */
export function fixForCommandError(error: Partial<CommandError> | null | undefined): string {
  const code = typeof error?.code === 'string' ? error.code : null;
  if (code && FIX_BY_CODE[code]) return `${FIX_BY_CODE[code]} (${code})`;
  const message = typeof error?.message === 'string' ? error.message.trim() : '';
  if (message) return code ? `${message} (${code})` : message;
  return code ? `${GENERIC_FIX} (${code})` : GENERIC_FIX;
}
