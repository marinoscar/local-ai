import { getLang } from "./i18n/i18n";

/** ApiError is a non-2xx answer. code is the doctor code of the agent's error body, when there is one. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** The one-sentence fix of the agent's error envelope, when it sent one. */
  readonly fix: string;
  constructor(status: number, code: string, message: string, fix = "") {
    super(message);
    this.status = status;
    this.code = code;
    this.fix = fix;
  }
}

/** The code of a session that ended (survival-portal.md section 5). */
export const CODE_SESSION_ENDED = "LAI-SVC-110";

let endedHandler: (() => void) | null = null;

/** onSessionEnded registers what runs when any call answers 401 LAI-SVC-110: refetch the status, so session-only buttons lock. */
export function onSessionEnded(fn: () => void): void {
  endedHandler = fn;
}

/**
 * apiFetch is the only way the Portal talks to the agent. It stays on this origin and sends the UI
 * language on every request (X-LocalAI-UI-Language, survival-portal.md section 4.3).
 */
export async function apiFetch(
  path: string,
  init: RequestInit = {},
): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("X-LocalAI-UI-Language", getLang());
  if (!headers.has("Accept")) headers.set("Accept", "application/json");
  const res = await fetch(path, {
    ...init,
    headers,
    credentials: "same-origin",
  });
  if (!res.ok) {
    let code = "";
    let message = res.statusText;
    let fix = "";
    try {
      const body = (await res.json()) as {
        error?: { code?: string; message?: string; fix?: string };
        code?: string;
      };
      code = body.error?.code ?? body.code ?? "";
      message = body.error?.message ?? message;
      fix = body.error?.fix ?? "";
    } catch {
      // Not JSON: keep the status text.
    }
    if (res.status === 401 && code === CODE_SESSION_ENDED) endedHandler?.();
    throw new ApiError(res.status, code, message, fix);
  }
  return res;
}

export async function apiJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await apiFetch(path, init);
  return (await res.json()) as T;
}

/** startSurvival is the banner button: POST /api/v1/mode/survival (the browser adds Origin). */
export async function startSurvival(): Promise<void> {
  await apiFetch("/api/v1/mode/survival", { method: "POST" });
}
