// The failure string of `POST …/commands/{id}/failure` (docs/specs/cloud-app.md section 3.6): EAB's
// `nodeJobFailureSchema` carries one string, `"LAI-CAT-005: Model does not fit in free VRAM"`. The cloud splits a
// leading doctor code from the message so the UI can show the code's plain-language fix; a string with no code is a
// failure the agent could not name, `LAI-CLD-012`.
import { scrubText } from './command-scrub';

/** The doctor code of a failure the agent reported without one (docs/specs/doctor-codes.md). */
export const LAI_CLD_COMMAND_FAILED = 'LAI-CLD-012';
/** The sweeper's code for a `queued` command nobody claimed before it expired. */
export const LAI_CLD_COMMAND_EXPIRED = 'LAI-CLD-030';
/** The sweeper's code for a `running` command whose lease passed. */
export const LAI_CLD_LEASE_LOST = 'LAI-CLD-059';

/** The longest stored message. */
export const MAX_FAILURE_MESSAGE = 500;

const LEADING_CODE = /^(LAI-[A-Z]{2,3}-\d{3}):\s*/;

/** What is stored in `localai_commands.error`. */
export interface CommandError {
  code: string;
  message: string;
}

/** Splits an agent failure string into `{ code, message }`; the message is scrubbed and at most 500 characters. */
export function parseFailureString(raw: string): CommandError {
  const match = LEADING_CODE.exec(raw);
  const code = match ? match[1] : LAI_CLD_COMMAND_FAILED;
  const rest = match ? raw.slice(match[0].length) : raw;
  return { code, message: scrubText(rest.trim()).slice(0, MAX_FAILURE_MESSAGE) };
}

/** The error the sweeper stores for an expired queued command. */
export const EXPIRED_ERROR: CommandError = {
  code: LAI_CLD_COMMAND_EXPIRED,
  message: 'The node did not pick this command up in time',
};

/** The error the sweeper stores for a command whose lease passed while it was running. */
export const LEASE_LOST_ERROR: CommandError = {
  code: LAI_CLD_LEASE_LOST,
  message: 'The node stopped renewing this command, so the cloud gave up waiting for it',
};
