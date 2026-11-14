import { describe, expect, it } from "vitest";
import { t } from "../i18n/i18n";
import {
  blockedSet,
  blockersOf,
  copyBlocked,
  driveWords,
  ejectBlocked,
  ejectOutcome,
  groupItems,
  groupOf,
  itemWords,
  rootLine,
  runEnded,
  runProgress,
  sizeText,
  syncBlocked,
  type Drive,
  type ReadyItem,
  type ReadyReport,
  type SyncRun,
} from "./content";

const item = (id: string, kind: string, status: string, extra: Partial<ReadyItem> = {}): ReadyItem => ({
  id,
  kind,
  status,
  ...extra,
});

function report(items: ReadyItem[], overall: ReadyReport["overall"] = "green"): ReadyReport {
  return { root: { id: "internal", kind: "internal" }, overall, last_sync: null, items, problems: [] };
}

const drive = (patch: Partial<Drive>): Drive => ({
  root_id: "ark-3f9c0a12",
  mount: "/media/operator/AI-ARK",
  state: "ready",
  label: "AI-ARK",
  ready: { overall: "green" },
  problems: [],
  ...patch,
});

describe("groups (dashboard.md section 4.3)", () => {
  it("puts each READY kind under its heading; whisper and piper are Speech", () => {
    expect(groupOf({ id: "osm-na", kind: "maps" })).toBe("maps");
    expect(groupOf({ id: "wikipedia-en", kind: "kiwix" })).toBe("library");
    expect(groupOf({ id: "qwen3.5-9b-q4km", kind: "model" })).toBe("models");
    expect(groupOf({ id: "whisper-small", kind: "model" })).toBe("speech");
    expect(groupOf({ id: "piper-es-medium", kind: "model" })).toBe("speech");
    expect(groupOf({ id: "kit-debs", kind: "kit" })).toBe("kit");
    expect(groupOf({ id: "docs", kind: "docs" })).toBe("docs");
    expect(groupOf({ id: "x", kind: "new-kind" })).toBe("other");
  });
  it("keeps the screen order and drops empty groups", () => {
    const g = groupItems([item("k", "kit", "verified"), item("m", "maps", "verified"), item("w", "kiwix", "missing")]);
    expect(g.map((x) => x.group)).toEqual(["maps", "library", "kit"]);
  });
});

describe("item status in plain words", () => {
  it.each([
    ["verified", undefined, "Ready", "ok"],
    ["missing", undefined, "Not downloaded yet", "bad"],
    ["quarantined", undefined, "Damaged copy set aside; it will be downloaded again", "bad"],
    ["skipped", "budget", "Skipped: it does not fit in the space set aside", "warn"],
    ["orphan", undefined, "Not in the plan; it can be removed to free space", "warn"],
  ])("%s (%s)", (status, reason, words, tone) => {
    const w = itemWords(item("a", "kiwix", status, { reason }));
    expect(w.tone).toBe(tone);
    expect(t(w.key)).toBe(words);
  });
  it("a version the last run skipped as blocked says so", () => {
    const run: SyncRun = {
      run_id: "r",
      trigger: "manual",
      state: "done",
      items: [{ root: "internal", item: "osm-na", result: "skipped", reason: "blocked", code: "LAI-SYN-020" }],
    };
    const b = blockedSet(run);
    expect(b.has("internal/osm-na")).toBe(true);
    expect(itemWords(item("osm-na", "maps", "missing"), b.has("internal/osm-na")).key).toBe("content.item.blocked");
  });
});

describe("root readiness line", () => {
  it("says ready when everything wanted is here and green", () => {
    expect(rootLine(report([item("a", "maps", "verified"), item("o", "maps", "orphan")]))).toBe("Internal disk: ready");
  });
  it("says ready with a warning for a stale item", () => {
    expect(rootLine(report([item("a", "maps", "stale")], "yellow"))).toBe("Internal disk: ready, with a warning");
  });
  it("counts what is missing and never says 100 %", () => {
    const many = Array.from({ length: 199 }, (_, i) => item(`v${i}`, "kiwix", "verified"));
    expect(rootLine(report([...many, item("m", "maps", "missing")], "red"))).toBe(
      "Internal disk: 99 % ready, 1 item missing",
    );
    expect(rootLine(report([item("a", "maps", "verified"), item("b", "maps", "failed"), item("c", "maps", "missing")], "red"))).toBe(
      "Internal disk: 33 % ready, 2 items missing",
    );
  });
});

describe("the run", () => {
  it("progress counts done, skipped and failed items and prefers bytes", () => {
    const run: SyncRun = {
      run_id: "r",
      trigger: "manual",
      state: "running",
      totals: { items: 10, done: 4, skipped: 1, failed: 0, bytes_total: 200, bytes_done: 50 },
    };
    expect(runProgress(run)).toEqual({ done: 5, total: 10, pct: 25 });
    expect(runProgress({ ...run, totals: undefined })).toBeNull();
  });
  it("an ended run says how it went", () => {
    const base: SyncRun = { run_id: "r", trigger: "manual", state: "done", result: "ok" };
    expect(runEnded(base).tone).toBe("ok");
    expect(runEnded({ ...base, result: "partial", totals: { items: 3, done: 1, skipped: 0, failed: 2, bytes_total: 0, bytes_done: 0 } }).text).toBe(
      "The last run finished, but 2 items did not work. The older copies are kept.",
    );
    expect(runEnded({ ...base, state: "cancelled", result: "cancelled" }).tone).toBe("warn");
    expect(runEnded({ ...base, state: "failed", result: "failed" }).tone).toBe("bad");
  });
});

describe("disabled buttons say why (dashboard.md section 5)", () => {
  it("Sync now needs online mode and no other run", () => {
    expect(syncBlocked("offline", false)).toBe("manage.needsInternet");
    expect(syncBlocked("survival", false)).toBe("manage.needsInternet");
    expect(syncBlocked("online", true)).toBe("content.why.busy");
    expect(syncBlocked("online", false)).toBeNull();
  });
  it("Copy from this drive needs a drive that can be read", () => {
    expect(copyBlocked(drive({}), false)).toBeNull();
    expect(copyBlocked(drive({ state: "read_only" }), false)).toBeNull();
    expect(copyBlocked(drive({}), true)).toBe("content.why.busy");
    expect(copyBlocked(drive({ state: "unclean" }), false)).toBe("content.why.unclean");
    expect(copyBlocked(drive({ state: "uninitialized" }), false)).toBe("content.why.empty");
    expect(copyBlocked(drive({ root_id: null, state: "unusable" }), false)).toBe("content.why.unusable");
  });
  it("Eject safely needs a mounted drive", () => {
    expect(ejectBlocked(drive({}))).toBeNull();
    expect(ejectBlocked(drive({ state: "uninitialized" }))).toBeNull();
    expect(ejectBlocked(drive({ state: "ejecting" }))).toBe("content.why.ejecting");
    expect(ejectBlocked(drive({ state: "not_mounted", mount: null }))).toBe("content.why.notMountedEject");
  });
});

describe("drives", () => {
  it("readiness in words", () => {
    expect(driveWords(drive({})).key).toBe("content.drive.ready");
    expect(driveWords(drive({ ready: null })).key).toBe("content.drive.readyUnchecked");
    expect(driveWords(drive({ ready: { overall: "red" } })).tone).toBe("bad");
    expect(driveWords(drive({ state: "uninitialized" })).key).toBe("content.drive.uninitialized");
  });
  it("an accepted eject is safe once the drive left the list or is not mounted", () => {
    const id = "ark-3f9c0a12";
    expect(ejectOutcome([drive({ state: "ejecting" })], id)).toBe("waiting");
    expect(ejectOutcome([], id)).toBe("safe");
    expect(ejectOutcome([drive({ state: "not_mounted", mount: null })], id)).toBe("safe");
    expect(ejectOutcome([drive({ state: "ready" })], id)).toBe("failed");
  });
  it("reads the blockers of a refused eject", () => {
    expect(blockersOf({ blockers: [{ kind: "model", name: "qwen", pid: 0 }, "junk"] })).toEqual([
      { kind: "model", name: "qwen", pid: 0 },
    ]);
    expect(blockersOf({})).toEqual([]);
  });
  it("sizes in decimal units", () => {
    expect(sizeText(5_870_000_000, "en")).toBe("5.9 GB");
    expect(sizeText(5_870_000_000, "es")).toBe("5,9 GB");
    expect(sizeText(2_000_398_934_016, "en")).toBe("2 TB");
    expect(sizeText(undefined, "en")).toBe("");
  });
});
