import { describe, expect, it } from "vitest";
import { parseScale, stepScale } from "./prefs";
import { screenOf } from "./router";
import {
  batteryLevel,
  StatusStore,
  statusLevel,
  type PortalStatus,
} from "./status";
import { viewFor } from "./view";

function status(over: Partial<PortalStatus> = {}): PortalStatus {
  return {
    schema_version: 1,
    mode: "survival",
    language: "en",
    emergency_number: "911",
    session: "portal",
    power: {
      on_ac: false,
      battery_pct: 72,
      minutes_left: 145,
      state: "discharging",
      low: false,
      critical: false,
    },
    clock_ok: true,
    brain: { loaded: [], loading: null },
    capabilities: {
      chat: { state: "ready" },
      library: { state: "ready" },
      maps: { state: "ready" },
    },
    first_aid: [],
    alerts: [],
    ...over,
  };
}

describe("statusLevel (survival-portal.md section 4.1.1)", () => {
  it("is green when everything is ready", () => {
    expect(statusLevel(status().capabilities)).toBe("green");
  });
  it("is amber when any capability is not ready", () => {
    expect(
      statusLevel({ chat: { state: "ready" }, photo: { state: "degraded" } }),
    ).toBe("amber");
    expect(
      statusLevel({ chat: { state: "ready" }, talk: { state: "unavailable" } }),
    ).toBe("amber");
  });
  it("is red when chat or library is unavailable", () => {
    expect(statusLevel({ chat: { state: "unavailable" } })).toBe("red");
    expect(
      statusLevel({
        chat: { state: "ready" },
        library: { state: "unavailable" },
        maps: { state: "degraded" },
      }),
    ).toBe("red");
  });
});

describe("batteryLevel", () => {
  const p = status().power;
  it("is ok on AC and above 20 %", () => {
    expect(batteryLevel({ ...p, on_ac: true, battery_pct: 5 })).toBe("ok");
    expect(batteryLevel({ ...p, battery_pct: 21 })).toBe("ok");
  });
  it("is low at 20 % and critical at 10 % on battery", () => {
    expect(batteryLevel({ ...p, battery_pct: 20 })).toBe("low");
    expect(batteryLevel({ ...p, battery_pct: 10 })).toBe("critical");
    expect(batteryLevel({ ...p, battery_pct: 50, critical: true })).toBe(
      "critical",
    );
  });
  it("is ok without a battery", () => {
    expect(batteryLevel({ ...p, battery_pct: null })).toBe("ok");
  });
});

describe("viewFor", () => {
  it("shows Home for the root", () => {
    expect(viewFor("", null)).toEqual({ kind: "home" });
  });
  it("locks session-only screens without a session, but not Library or Maps", () => {
    const s = status({ session: "none" });
    for (const sc of ["chat", "photo", "talk", "brain"] as const)
      expect(viewFor(sc, s).kind, sc).toBe("lock");
    expect(viewFor("library", s)).toEqual({
      kind: "screen",
      screen: "library",
    });
    expect(viewFor("maps", s)).toEqual({ kind: "screen", screen: "maps" });
  });
  it("explains an unavailable capability instead of hiding it", () => {
    const s = status({
      capabilities: { library: { state: "unavailable", code: "LAI-SVC-001" } },
    });
    expect(viewFor("library", s)).toEqual({
      kind: "unavailable",
      cap: "library",
    });
  });
  it("lets Library and Maps open when contact is lost", () => {
    expect(viewFor("library", null)).toEqual({
      kind: "screen",
      screen: "library",
    });
  });
});

describe("router and prefs", () => {
  it("maps paths to screens", () => {
    expect(screenOf("/portal")).toBe("");
    expect(screenOf("/portal/")).toBe("");
    expect(screenOf("/portal/talk")).toBe("talk");
    expect(screenOf("/portal/first-aid")).toBe("first-aid");
    expect(screenOf("/portal/nope")).toBe("");
  });
  it("steps the text scale and stops at the ends", () => {
    expect(stepScale(1, 1)).toBe(1.25);
    expect(stepScale(1.5, 1)).toBe(1.5);
    expect(stepScale(1, -1)).toBe(1);
    expect(parseScale("1.25")).toBe(1.25);
    expect(parseScale("7")).toBe(1);
    expect(parseScale(null)).toBe(1);
  });
});

describe("StatusStore", () => {
  function harness(results: (PortalStatus | Error)[]) {
    const ticks: (() => void)[] = [];
    const opened: string[] = [];
    const store = new StatusStore({
      fetchStatus: async () => {
        const r = results.shift() as PortalStatus | Error;
        if (r instanceof Error) throw r;
        return r;
      },
      openEvents: () => {
        opened.push("open");
        const handlers: Record<string, () => void> = {};
        return {
          addEventListener: (t, f) => (handlers[t] = f),
          close: () => opened.push("close"),
        };
      },
      setInterval: (fn) => ticks.push(fn),
      clearInterval: () => undefined,
    });
    return { store, ticks, opened };
  }
  it("flags lost contact on a failed fetch, keeps the last body, and recovers", async () => {
    const h = harness([
      status(),
      new Error("down"),
      status({ language: "es" }),
    ]);
    h.store.start();
    await h.store.refetch();
    expect(h.store.getSnapshot().phase).toBe("lost");
    expect(h.store.getSnapshot().status?.language).toBe("en");
    await h.store.refetch();
    expect(h.store.getSnapshot().phase).toBe("ok");
    h.store.stop();
  });
  it("opens the event stream only with a session", async () => {
    const h = harness([
      status({ session: "none" }),
      status({ session: "portal" }),
      status({ session: "portal" }),
    ]);
    await h.store.refetch();
    expect(h.opened).toEqual([]);
    await h.store.refetch();
    await h.store.refetch();
    expect(h.opened).toEqual(["open"]);
  });
});
