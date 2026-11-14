import { describe, expect, it } from "vitest";
import {
  buttons,
  deletePath,
  downloadView,
  formatSize,
  groups,
  rowState,
  type ButtonContext,
  type ModelRow,
} from "./models";

function row(p: Partial<ModelRow>): ModelRow {
  return {
    id: "m",
    display_name: "M",
    engine: "llama",
    in_profile: true,
    size_bytes: 5_868_826_976,
    present: true,
    verified: true,
    locations: [{ root: "/srv/localai", complete: true }],
    loadable: true,
    loadable_reason: null,
    status: "stored",
    loaded: false,
    default: false,
    ...p,
  };
}

const ctx: ButtonContext = { mode: "online", busy: false, downloading: false, downloadRunning: false };
const shown = (r: ReturnType<typeof buttons>) =>
  Object.fromEntries(Object.entries(r).filter(([, v]) => v.show).map(([k, v]) => [k, v.show ? v.why : null]));

describe("rowState (agent.md section 3.8.7 status)", () => {
  it("says each status in plain words", () => {
    expect(rowState(row({ status: "loaded" }))).toBe("in_use");
    expect(rowState(row({ status: "sleeping" }))).toBe("in_use");
    expect(rowState(row({ status: "loading" }))).toBe("loading");
    expect(rowState(row({ status: "stored" }))).toBe("on_computer");
    expect(rowState(row({ status: "unloaded" }))).toBe("on_computer");
    expect(rowState(row({ status: "failed" }))).toBe("failed");
    expect(rowState(row({ status: "absent", present: false }))).toBe("absent");
    expect(rowState(row({ status: "n/a", engine: "whisper" }))).toBe("on_computer");
    expect(rowState(row({ status: "n/a", engine: "piper", present: false }))).toBe("absent");
  });
});

describe("buttons (dashboard.md sections 3.2 and 5)", () => {
  it("a brain in use offers Unload, Make default; Delete says unload first", () => {
    expect(shown(buttons(row({ status: "loaded", loaded: true }), ctx))).toEqual({
      unload: null,
      makeDefault: null,
      delete: "models.why.unloadFirst",
    });
  });
  it("a brain on disk offers Use this brain and Delete; the default says it is already", () => {
    expect(shown(buttons(row({ default: true }), ctx))).toEqual({
      use: null,
      makeDefault: "models.why.isDefault",
      delete: null,
    });
  });
  it("a brain that does not fit says why on Use, Make default and Download", () => {
    const big = { loadable: false, loadable_reason: { code: "LAI-CAT-005", message: "Needs 11 GB" } };
    expect(shown(buttons(row(big), ctx))).toEqual({
      use: "models.why.tooBig",
      makeDefault: "models.why.tooBig",
      delete: null,
    });
    expect(shown(buttons(row({ ...big, present: false, status: "absent", locations: [] }), ctx))).toEqual({
      makeDefault: "models.why.downloadFirst",
      download: "models.why.tooBig",
    });
  });
  it("Download needs the internet: disabled with the reason offline and in survival", () => {
    const absent = row({ present: false, status: "absent", locations: [], loadable: false, loadable_reason: { code: "LAI-LLM-008", message: "" } });
    expect(buttons(absent, ctx).download).toEqual({ show: true, why: null });
    for (const mode of ["offline", "survival"])
      expect(buttons(absent, { ...ctx, mode }).download).toEqual({ show: true, why: "manage.needsInternet" });
    expect(buttons(absent, { ...ctx, downloadRunning: true }).download).toEqual({ show: true, why: "models.why.downloadRunning" });
    expect(buttons(absent, { ...ctx, downloading: true, downloadRunning: true }).download).toEqual({ show: false });
  });
  it("while another task runs every enabled button waits", () => {
    const r = shown(buttons(row({}), { ...ctx, busy: true }));
    expect(r).toEqual({ use: "models.why.busy", makeDefault: "models.why.busy", delete: "models.why.busy" });
  });
  it("a speech model only downloads or deletes", () => {
    expect(shown(buttons(row({ engine: "whisper", status: "n/a" }), ctx))).toEqual({ delete: null });
  });
  it("a router that is down says so", () => {
    const r = buttons(row({ loadable: false, loadable_reason: { code: "LAI-LLM-004", message: "" } }), ctx);
    expect(r.use).toEqual({ show: true, why: "models.why.serviceDown" });
  });
});

describe("groups", () => {
  it("puts brains in use first, the rest of the catalog in More, speech apart", () => {
    const g = groups([
      row({ id: "a" }),
      row({ id: "b", status: "loaded", loaded: true }),
      row({ id: "c", present: false, status: "absent", in_profile: false }),
      row({ id: "d", engine: "piper", status: "n/a" }),
      row({ id: "e", present: false, status: "absent" }),
    ]);
    expect(g.brains.map((m) => m.id)).toEqual(["b", "a", "e"]);
    expect(g.more.map((m) => m.id)).toEqual(["c"]);
    expect(g.speech.map((m) => m.id)).toEqual(["d"]);
  });
});

describe("formatSize and deletePath", () => {
  it("formats decimal sizes in both languages", () => {
    expect(formatSize(5_868_826_976, "en")).toBe("5.9 GB");
    expect(formatSize(5_868_826_976, "es")).toBe("5,9 GB");
    expect(formatSize(466_000_000, "en")).toBe("466 MB");
    expect(formatSize(0, "en")).toBe("");
  });
  it("deletes from this computer's own disk, keeping the last copy unless told", () => {
    expect(deletePath("qwen 3", true)).toBe("/models/qwen%203?root=internal&keep_last=true");
    expect(deletePath("x", false)).toBe("/models/x?root=internal&keep_last=false");
  });
});

describe("downloadView (ark-sync.md section 3.5)", () => {
  it("reads the percent of the model's transfer", () => {
    const run = { run_id: "r1", state: "running", active: [{ item: "m", bytes_done: 25, bytes_total: 100 }] };
    expect(downloadView(run, "r1", "m")).toEqual({ kind: "running", percent: 25 });
    expect(downloadView({ ...run, active: [] }, "r1", "m")).toEqual({ kind: "running", percent: null });
  });
  it("another run, or none yet, is still running", () => {
    expect(downloadView(null, "r1", "m")).toEqual({ kind: "running", percent: null });
    expect(downloadView({ run_id: "r0", state: "done" }, "r1", "m")).toEqual({ kind: "running", percent: null });
  });
  it("an ended run says ok, or the code of the model's item", () => {
    expect(downloadView({ run_id: "r1", state: "done", items: [{ item: "m", result: "ok" }] }, "r1", "m")).toEqual({ kind: "ok" });
    expect(
      downloadView({ run_id: "r1", state: "done", items: [{ item: "m", result: "failed", code: "LAI-SYN-020" }] }, "r1", "m"),
    ).toEqual({ kind: "failed", code: "LAI-SYN-020" });
    expect(downloadView({ run_id: "r1", state: "cancelled", code: "LAI-SYN-002" }, "r1", "m")).toEqual({
      kind: "failed",
      code: "LAI-SYN-002",
    });
  });
});
