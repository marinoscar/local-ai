import { ApiError, apiSend } from "../api";

// The admin PIN of a Manage button (dashboard.md section 2, "PIN action"): POST /pin/verify answers a
// grant that is valid for 5 minutes (agent.md section 3.8.3); it is kept in memory only, never stored,
// and reused until 30 s before it expires so a second delete does not ask again at once.

export interface Grant {
  grant: string;
  expires_at: string;
}

let cached: { grant: string; until: number } | null = null;

/** cachedGrant is the grant still usable at `now`, or null. */
export function cachedGrant(now = Date.now()): string | null {
  return cached && cached.until > now ? cached.grant : null;
}

export function forgetGrant(): void {
  cached = null;
}

/** What a PIN attempt came to, in the words the dialog shows. */
export type PinResult =
  | { ok: true; grant: string }
  | { ok: false; kind: "wrong"; remaining: number | null }
  | { ok: false; kind: "locked"; retryAfterS: number }
  | { ok: false; kind: "not_set" }
  | { ok: false; kind: "error"; code: string; message: string };

export async function verifyPin(
  pin: string,
  send: typeof apiSend = apiSend,
  now = Date.now(),
): Promise<PinResult> {
  try {
    const g = await send<Grant>("POST", "/pin/verify", { pin });
    if (!g) return { ok: false, kind: "error", code: "", message: "empty answer" };
    const exp = Date.parse(g.expires_at);
    cached = {
      grant: g.grant,
      until: (Number.isNaN(exp) ? now + 300_000 : exp) - 30_000,
    };
    return { ok: true, grant: g.grant };
  } catch (e) {
    if (e instanceof ApiError) {
      if (e.code === "LAI-SEC-003") {
        const r = e.details["remaining_attempts"];
        return { ok: false, kind: "wrong", remaining: typeof r === "number" ? r : null };
      }
      if (e.code === "LAI-SEC-004") {
        const s = e.details["retry_after_s"];
        return { ok: false, kind: "locked", retryAfterS: typeof s === "number" ? s : 60 };
      }
      if (e.code === "LAI-SEC-005") return { ok: false, kind: "not_set" };
      return { ok: false, kind: "error", code: e.code, message: e.message };
    }
    return {
      ok: false,
      kind: "error",
      code: "",
      message: e instanceof Error ? e.message : String(e),
    };
  }
}
