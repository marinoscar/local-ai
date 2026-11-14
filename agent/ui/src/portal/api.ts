import { getLang } from "./i18n/i18n";

/** ApiError is a non-2xx answer. code is the doctor code of the agent's error body, when there is one. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** The one-sentence fix of the agent's error envelope, when it sent one. */
  readonly fix: string;
  /** The envelope's details (remaining_attempts, retry_after_s, pin_required …); {} when none. */
  readonly details: Record<string, unknown>;
  constructor(
    status: number,
    code: string,
    message: string,
    fix = "",
    details: Record<string, unknown> = {},
  ) {
    super(message);
    this.status = status;
    this.code = code;
    this.fix = fix;
    this.details = details;
  }
}

/** The code of a session that ended (survival-portal.md section 5). */
export const CODE_SESSION_ENDED = "LAI-SVC-110";

/** The code of the lost-contact banner (survival-portal.md section 4.2): the agent is not answering, so its text is the Portal's own. */
export const CODE_LOST_CONTACT = "LAI-SVC-116";

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
    let details: Record<string, unknown> = {};
    try {
      const body = (await res.json()) as {
        error?: {
          code?: string;
          message?: string;
          fix?: string;
          details?: Record<string, unknown>;
        };
        code?: string;
      };
      code = body.error?.code ?? body.code ?? "";
      message = body.error?.message ?? message;
      fix = body.error?.fix ?? "";
      details = body.error?.details ?? {};
    } catch {
      // Not JSON: keep the status text.
    }
    if (res.status === 401 && code === CODE_SESSION_ENDED) endedHandler?.();
    throw new ApiError(res.status, code, message, fix, details);
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

/** The header that lifts an operator request to admin with a PIN grant (agent.md section 3.9 step 6). */
export const PIN_GRANT_HEADER = "X-LocalAI-Pin-Grant";

/**
 * apiSend is a JSON call of a Manage button: method, path under /api/v1, an optional body and an
 * optional PIN grant. It answers the parsed body, or null when the answer has none (204).
 */
export async function apiSend<T = unknown>(
  method: "GET" | "POST" | "PUT" | "DELETE",
  path: string,
  body?: unknown,
  grant?: string,
): Promise<T | null> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (grant) headers[PIN_GRANT_HEADER] = grant;
  const res = await apiFetch(`/api/v1${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  return text === "" ? null : (JSON.parse(text) as T);
}

/** A failure in plain words for a screen: the code, the message and the one-sentence fix. */
export interface Failure {
  code: string;
  message: string;
  fix: string;
}

export function failureOf(e: unknown): Failure {
  if (e instanceof ApiError)
    return { code: e.code, message: e.message, fix: e.fix };
  return {
    code: "",
    message: e instanceof Error ? e.message : String(e),
    fix: "",
  };
}
