import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import {
  EnsureStore,
  POLL_BUSY_MS,
  POLL_IDLE_MS,
  STATE_MARK,
  cancelEnsure,
  canInstall,
  startEnsure,
  stepsNeedingOk,
  type EnsureStep,
  type EnsureView,
} from "./ensure";
import en from "./i18n/en.json";
import es from "./i18n/es.json";

function step(id: string, status: EnsureStep["status"], extra: Partial<EnsureStep> = {}): EnsureStep {
  return { id, status, needs_confirmation: false, title: id, detail: "", ...extra };
}

function view(state: EnsureView["state"], steps: EnsureStep[] = []): EnsureView {
  return {
    state,
    auto: "on",
    message: state,
    next_action: { kind: "none", text: "" },
    progress: { done: 0, total: steps.length },
    steps,
    run: null,
    dry_run: false,
    checked_at: "2026-11-10T10:00:00Z",
  };
}

describe("canInstall", () => {
  it("is true only when something is left and no pass runs", () => {
    expect(canInstall(view("needs_you", [step("tool.piper", "pending")]))).toBe(true);
    expect(canInstall(view("off", [step("tool.piper", "waiting")]))).toBe(true);
    expect(canInstall(view("needs_you", [step("tool.piper", "failed")]))).toBe(true);
    expect(canInstall(view("installing", [step("tool.piper", "running")]))).toBe(false);
    expect(canInstall(view("complete", [step("tool.piper", "ok")]))).toBe(false);
  });
  it("is false when only a person's OK or the kit is missing", () => {
    expect(
      canInstall(
        view("needs_you", [step("whisper.build", "needs_confirm"), step("tool.pmtiles", "blocked")]),
      ),
    ).toBe(false);
  });
});

describe("stepsNeedingOk", () => {
  it("lists the steps with status needs_confirm", () => {
    const v = view("needs_you", [step("tool.piper", "ok"), step("restart.kiwix", "needs_confirm")]);
    expect(stepsNeedingOk(v).map((s) => s.id)).toEqual(["restart.kiwix"]);
  });
});

describe("strings", () => {
  it("every state, step status and warning has English and Spanish text", () => {
    for (const k of Object.keys(STATE_MARK)) {
      expect(en[`ensure.state.${k}` as keyof typeof en], k).toBeTruthy();
      expect(es[`ensure.state.${k}` as keyof typeof es], k).toBeTruthy();
    }
    for (const k of [
      "ok",
      "pending",
      "waiting",
      "needs_confirm",
      "blocked",
      "running",
      "done",
      "failed",
      "cancelled",
    ]) {
      expect(en[`ensure.step.${k}` as keyof typeof en], k).toBeTruthy();
      expect(es[`ensure.step.${k}` as keyof typeof es], k).toBeTruthy();
    }
    for (const k of ["whisper.build", "restart.kiwix"]) {
      expect(en[`ensure.warn.${k}` as keyof typeof en], k).toBeTruthy();
      expect(es[`ensure.warn.${k}` as keyof typeof es], k).toBeTruthy();
    }
    expect(en["fix.ensure"]).toBe("Install what's missing");
    expect(es["fix.ensure"]).toBe("Instalar lo que falta");
  });
});

describe("startEnsure and cancelEnsure", () => {
  it("POST {} (the Portal's safe default) and report running on 202", async () => {
    const calls: [string, RequestInit | undefined][] = [];
    const fetcher = async (path: string, init?: RequestInit) => {
      calls.push([path, init]);
      return new Response(JSON.stringify(view("installing")), { status: 202 });
    };
    const res = await startEnsure(fetcher);
    expect(res.ok && res.running && res.view.state).toBe("installing");
    expect(calls[0][0]).toBe("/api/v1/ensure");
    expect(calls[0][1]?.method).toBe("POST");
    expect(calls[0][1]?.body).toBe("{}");
    expect(new Headers(calls[0][1]?.headers).get("Accept-Language")).toBe("en");
  });
  it("a 200 (nothing to do) is not running", async () => {
    const fetcher = async () => new Response(JSON.stringify(view("complete")), { status: 200 });
    const res = await startEnsure(fetcher);
    expect(res.ok && res.running).toBe(false);
  });
  it("DELETE cancels", async () => {
    const calls: [string, RequestInit | undefined][] = [];
    const fetcher = async (path: string, init?: RequestInit) => {
      calls.push([path, init]);
      return new Response(JSON.stringify(view("needs_you")), { status: 202 });
    };
    const res = await cancelEnsure(fetcher);
    expect(res.ok).toBe(true);
    expect(calls[0][1]?.method).toBe("DELETE");
    expect(calls[0][1]?.body).toBeUndefined();
  });
  it.each([
    [409, "LAI-SYN-005"],
    [409, "LAI-AGT-022"],
    [503, "LAI-AGT-024"],
    [403, "LAI-SEC-002"],
  ])("a %i %s refusal keeps its code, message and fix", async (status, code) => {
    const fetcher = async (): Promise<Response> => {
      throw new ApiError(status, code, "msg", "fix it");
    };
    expect(await startEnsure(fetcher)).toEqual({
      ok: false,
      status,
      code,
      message: "msg",
      fix: "fix it",
    });
  });
  it("a network failure has no code", async () => {
    const fetcher = async (): Promise<Response> => {
      throw new Error("boom");
    };
    expect(await startEnsure(fetcher)).toEqual({
      ok: false,
      status: 0,
      code: "",
      message: "boom",
      fix: "",
    });
  });
});

describe("EnsureStore", () => {
  function setup(fetchView: () => Promise<EnsureView>) {
    const timers: { fn: () => void; ms: number; live: boolean }[] = [];
    const store = new EnsureStore({
      fetchView,
      setTimeout: (fn, ms) => {
        const t = { fn, ms, live: true };
        timers.push(t);
        return t;
      },
      clearTimeout: (h) => {
        (h as { live: boolean }).live = false;
      },
    });
    return {
      store,
      timers,
      lastMs: () => timers.filter((t) => t.live).at(-1)?.ms,
    };
  }

  it("polls every second while installing and every five seconds otherwise", async () => {
    let v = view("installing", [step("tool.piper", "running")]);
    const s = setup(() => Promise.resolve(v));
    s.store.start();
    await Promise.resolve();
    await Promise.resolve();
    expect(s.store.getSnapshot().phase).toBe("ok");
    expect(s.lastMs()).toBe(POLL_BUSY_MS);
    v = view("complete");
    await s.store.refetch();
    expect(s.lastMs()).toBe(POLL_IDLE_MS);
    expect(s.timers.filter((t) => t.live)).toHaveLength(1);
  });

  it("keeps the last good picture when the agent stops answering, and retries", async () => {
    let fail = false;
    const s = setup(() => (fail ? Promise.reject(new Error("down")) : Promise.resolve(view("complete"))));
    s.store.start();
    await Promise.resolve();
    await Promise.resolve();
    fail = true;
    await s.store.refetch();
    const snap = s.store.getSnapshot();
    expect(snap.phase).toBe("unavailable");
    expect(snap.view?.state).toBe("complete");
    expect(s.lastMs()).toBe(POLL_IDLE_MS);
  });

  it("accept shows the answer of the button at once and schedules the next poll", () => {
    const s = setup(() => new Promise(() => undefined));
    s.store.start();
    s.store.accept(view("installing"));
    expect(s.store.getSnapshot().view?.state).toBe("installing");
    expect(s.lastMs()).toBe(POLL_BUSY_MS);
  });

  it("stop ends polling and ignores a late answer", async () => {
    let resolve: (v: EnsureView) => void = () => undefined;
    const s = setup(() => new Promise<EnsureView>((r) => (resolve = r)));
    s.store.start();
    s.store.stop();
    resolve(view("complete"));
    await Promise.resolve();
    await Promise.resolve();
    expect(s.store.getSnapshot().phase).toBe("loading");
    expect(s.timers.filter((t) => t.live)).toHaveLength(0);
  });
});
