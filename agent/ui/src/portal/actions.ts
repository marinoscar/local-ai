import { ApiError, apiFetch } from "./api";
import type { Key } from "./i18n/i18n";

// The buttons of a fix (doctor-codes.md section 1.7, survival-portal.md sections 4.2 and 4.1.8). The
// agent puts `action` on an alert or a capability whose code has one; the Portal shows the button,
// with the typed command as a small second line for whoever prefers the terminal.

export type FixAction =
  | "ark.import"
  | "service.restart:kiwix"
  | "service.restart:whisper"
  | "portal.brain";

interface ActionInfo {
  /** The button label. */
  label: Key;
  /** The typed command shown under the button; "" when there is none (a Portal screen). */
  command: string;
  /** The POST the button makes, or null for a screen of the Portal. */
  path: string | null;
  /** What the button says when the POST answered. */
  done: Key;
}

export const ACTIONS: Record<FixAction, ActionInfo> = {
  "ark.import": {
    label: "fix.import",
    command: "aictl ark import",
    path: "/api/v1/ark/import",
    done: "fix.importStarted",
  },
  "service.restart:kiwix": {
    label: "fix.restartLibrary",
    command: "aictl services restart kiwix",
    path: "/api/v1/services/kiwix/restart",
    done: "fix.restarted",
  },
  "service.restart:whisper": {
    label: "fix.restartSpeech",
    command: "aictl services restart whisper",
    path: "/api/v1/services/whisper/restart",
    done: "fix.restarted",
  },
  "portal.brain": {
    label: "fix.brain",
    command: "",
    path: null,
    done: "fix.brain",
  },
};

/** isFixAction: only the closed set is shown; an action this build does not know is ignored. */
export function isFixAction(v: unknown): v is FixAction {
  return typeof v === "string" && Object.prototype.hasOwnProperty.call(ACTIONS, v);
}

export type FixResult =
  | { ok: true }
  | { ok: false; code: string; message: string; fix: string };

/**
 * runFix makes the POST of an action. The body is `{}`: the agent replaces it with its safe default
 * for a Portal session (survival-portal.md section 3.3). A refusal (429 LAI-SVC-117, 409 LAI-SVC-118,
 * a 502 of the service) comes back with its code, message and fix.
 */
export async function runFix(
  action: FixAction,
  fetcher: typeof apiFetch = apiFetch,
): Promise<FixResult> {
  const path = ACTIONS[action].path;
  if (path === null) return { ok: true };
  try {
    await fetcher(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    return { ok: true };
  } catch (e) {
    if (e instanceof ApiError)
      return { ok: false, code: e.code, message: e.message, fix: e.fix };
    return {
      ok: false,
      code: "",
      message: e instanceof Error ? e.message : String(e),
      fix: "",
    };
  }
}
