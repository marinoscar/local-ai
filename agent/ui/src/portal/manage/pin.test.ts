import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { cachedGrant, forgetGrant, verifyPin } from "./pin";

type Send = Parameters<typeof verifyPin>[1];

function answering(result: unknown | ApiError): { send: Send; calls: unknown[][] } {
  const calls: unknown[][] = [];
  const send = (async (...args: unknown[]) => {
    calls.push(args);
    if (result instanceof ApiError) throw result;
    return result;
  }) as Send;
  return { send, calls };
}

describe("verifyPin (agent.md section 3.8.3)", () => {
  it("POSTs the PIN and keeps the grant until 30 s before it expires", async () => {
    forgetGrant();
    const now = Date.parse("2026-11-14T12:00:00Z");
    const { send, calls } = answering({ grant: "pg_x", expires_at: "2026-11-14T12:05:00Z" });
    expect(await verifyPin("4821", send, now)).toEqual({ ok: true, grant: "pg_x" });
    expect(calls[0]).toEqual(["POST", "/pin/verify", { pin: "4821" }]);
    expect(cachedGrant(now + 60_000)).toBe("pg_x");
    expect(cachedGrant(now + 271_000)).toBeNull();
  });
  it("says how many tries are left after a wrong PIN", async () => {
    forgetGrant();
    const e = new ApiError(401, "LAI-SEC-003", "wrong", "", { remaining_attempts: 3 });
    expect(await verifyPin("1", answering(e).send)).toEqual({ ok: false, kind: "wrong", remaining: 3 });
    expect(cachedGrant()).toBeNull();
  });
  it("tells the wait after too many tries, and when no PIN is set", async () => {
    const locked = new ApiError(429, "LAI-SEC-004", "locked", "", { retry_after_s: 120 });
    expect(await verifyPin("1", answering(locked).send)).toEqual({ ok: false, kind: "locked", retryAfterS: 120 });
    const unset = new ApiError(409, "LAI-SEC-005", "no pin");
    expect(await verifyPin("1", answering(unset).send)).toEqual({ ok: false, kind: "not_set" });
  });
  it("keeps any other failure's code", async () => {
    const down = new ApiError(503, "LAI-AGT-024", "starting");
    expect(await verifyPin("1", answering(down).send)).toEqual({
      ok: false,
      kind: "error",
      code: "LAI-AGT-024",
      message: "starting",
    });
  });
});
