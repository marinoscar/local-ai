import { describe, expect, it } from "vitest";
import { ACTIONS, isFixAction, runFix } from "./actions";
import { ApiError } from "./api";
import { translate } from "./i18n/i18n";

describe("fix actions", () => {
  it("are the closed set of doctor-codes.md section 1.7", () => {
    expect(Object.keys(ACTIONS).sort()).toEqual([
      "ark.import",
      "ensure.run",
      "portal.brain",
      "service.restart:kiwix",
      "service.restart:whisper",
    ]);
    expect(isFixAction("ark.import")).toBe(true);
    expect(isFixAction("service.restart:llama")).toBe(false);
    expect(isFixAction("toString")).toBe(false);
    expect(isFixAction(undefined)).toBe(false);
  });

  it("have a label in both languages and never reach llama or a stop", () => {
    for (const a of Object.values(ACTIONS)) {
      expect(translate("en", a.label)).not.toBe(a.label);
      expect(translate("es", a.label)).not.toBe(a.label);
      expect(translate("en", a.done)).not.toBe(a.done);
      if (a.path !== null)
        expect(a.path).toMatch(
          /^\/api\/v1\/(ark\/import|ensure|services\/(kiwix|whisper)\/restart)$/,
        );
    }
  });

  it("POST {} to the route of the action", async () => {
    const calls: [string, RequestInit | undefined][] = [];
    const fetcher = async (path: string, init?: RequestInit) => {
      calls.push([path, init]);
      return new Response("{}", { status: 202 });
    };
    expect(await runFix("ark.import", fetcher)).toEqual({ ok: true });
    expect(await runFix("service.restart:whisper", fetcher)).toEqual({
      ok: true,
    });
    expect(calls.map((c) => c[0])).toEqual([
      "/api/v1/ark/import",
      "/api/v1/services/whisper/restart",
    ]);
    expect(calls[0][1]?.method).toBe("POST");
    expect(calls[0][1]?.body).toBe("{}");
  });

  it("ensure.run POSTs {} to /ensure and its typed command is aictl ensure", async () => {
    const calls: [string, RequestInit | undefined][] = [];
    const fetcher = async (path: string, init?: RequestInit) => {
      calls.push([path, init]);
      return new Response("{}", { status: 202 });
    };
    expect(await runFix("ensure.run", fetcher)).toEqual({ ok: true });
    expect(calls[0][0]).toBe("/api/v1/ensure");
    expect(calls[0][1]?.method).toBe("POST");
    expect(calls[0][1]?.body).toBe("{}");
    expect(ACTIONS["ensure.run"].command).toBe("aictl ensure");
  });

  it("the brain action calls nothing", async () => {
    let called = false;
    const fetcher = async () => {
      called = true;
      return new Response("{}");
    };
    expect(await runFix("portal.brain", fetcher)).toEqual({ ok: true });
    expect(called).toBe(false);
  });

  it("a refusal keeps its code, message and fix", async () => {
    const fetcher = async (): Promise<Response> => {
      throw new ApiError(429, "LAI-SVC-117", "Too many repair attempts", "Wait a few minutes.");
    };
    expect(await runFix("service.restart:kiwix", fetcher)).toEqual({
      ok: false,
      code: "LAI-SVC-117",
      message: "Too many repair attempts",
      fix: "Wait a few minutes.",
    });
  });
});
