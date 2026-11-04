import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import {
  BrainSwitchMachine,
  LOAD_TIMEOUT_MS,
  loadBody,
  type SwitchDeps,
  type SwitchEvent,
} from "./useBrainSwitch";

class FakeSource {
  handlers = new Map<string, (e: SwitchEvent) => void>();
  closed = false;
  addEventListener(type: string, fn: (e: SwitchEvent) => void) {
    this.handlers.set(type, fn);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data: Record<string, unknown>) {
    this.handlers.get(type)?.({ data: JSON.stringify({ type, data }) });
  }
}

function setup(post: SwitchDeps["post"] = () => Promise.resolve()) {
  const es = new FakeSource();
  let now = 1000;
  let tick: (() => void) | null = null;
  const posted: string[] = [];
  const deps: SwitchDeps = {
    post: (id) => {
      posted.push(id);
      return post(id);
    },
    openEvents: () => es,
    now: () => now,
    setInterval: (fn) => {
      tick = fn;
      return 1;
    },
    clearInterval: () => {
      tick = null;
    },
  };
  const m = new BrainSwitchMachine("vlm", deps);
  return {
    m,
    es,
    posted,
    advance(ms: number) {
      now += ms;
      tick?.();
    },
    hasTimer: () => tick !== null,
  };
}

describe("BrainSwitchMachine", () => {
  it("goes idle -> confirming -> loading -> loaded", async () => {
    const s = setup();
    expect(s.m.getSnapshot().phase).toBe("idle");
    s.m.ask();
    expect(s.m.getSnapshot().phase).toBe("confirming");
    await s.m.confirm();
    expect(s.posted).toEqual(["vlm"]);
    expect(s.m.getSnapshot()).toEqual({ phase: "loading", seconds: 0 });
    s.advance(35_000);
    expect(s.m.getSnapshot()).toEqual({ phase: "loading", seconds: 35 });
    s.es.emit("model.loaded", { model: "vlm", load_ms: 35000 });
    expect(s.m.getSnapshot().phase).toBe("loaded");
    expect(s.es.closed).toBe(true);
    expect(s.hasTimer()).toBe(false);
  });

  it("cancel from the confirmation posts nothing", () => {
    const s = setup();
    s.m.ask();
    s.m.cancel();
    expect(s.m.getSnapshot().phase).toBe("idle");
    expect(s.posted).toEqual([]);
  });

  it("confirm without ask does nothing", async () => {
    const s = setup();
    await s.m.confirm();
    expect(s.posted).toEqual([]);
  });

  it("ignores events of another model", async () => {
    const s = setup();
    s.m.ask();
    await s.m.confirm();
    s.es.emit("model.loaded", { model: "other" });
    s.es.emit("model.failed", { model: "other", message: "x" });
    expect(s.m.getSnapshot().phase).toBe("loading");
  });

  it("model.failed gives LAI-SVC-107", async () => {
    const s = setup();
    s.m.ask();
    await s.m.confirm();
    s.es.emit("model.failed", {
      model: "vlm",
      code: "LAI-RTR-005",
      message: "out of memory",
      rolled_back: true,
    });
    expect(s.m.getSnapshot()).toMatchObject({
      phase: "failed",
      kind: "failed",
      code: "LAI-SVC-107",
      message: "out of memory",
    });
  });

  it("times out after 300 s with a fake clock", async () => {
    const s = setup();
    s.m.ask();
    await s.m.confirm();
    s.advance(LOAD_TIMEOUT_MS - 1000);
    expect(s.m.getSnapshot().phase).toBe("loading");
    s.advance(1000);
    expect(s.m.getSnapshot()).toMatchObject({
      phase: "failed",
      kind: "timeout",
      code: "LAI-SVC-107",
    });
    expect(s.es.closed).toBe(true);
  });

  it("a rejected request (429) keeps the agent's code and fix", async () => {
    const s = setup(() =>
      Promise.reject(
        new ApiError(429, "LAI-SVC-114", "Too fast", "Wait a minute."),
      ),
    );
    s.m.ask();
    await s.m.confirm();
    expect(s.m.getSnapshot()).toMatchObject({
      phase: "failed",
      kind: "rejected",
      code: "LAI-SVC-114",
      fix: "Wait a minute.",
    });
  });

  it("a status poll that shows the model loaded finishes the switch", async () => {
    const s = setup();
    s.m.ask();
    await s.m.confirm();
    s.m.notifyLoaded();
    expect(s.m.getSnapshot().phase).toBe("loaded");
  });

  it("can try again after a failure", async () => {
    const s = setup();
    s.m.ask();
    await s.m.confirm();
    s.es.emit("model.failed", { model: "vlm" });
    s.m.ask();
    expect(s.m.getSnapshot().phase).toBe("confirming");
  });
});

describe("loadBody", () => {
  it("portal session sends wait only; full session also unloads the others", () => {
    expect(loadBody("portal")).toEqual({ wait: false });
    expect(loadBody("full")).toEqual({ wait: false, unload_others: true });
  });
});
