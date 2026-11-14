import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { contentApi, loadContent } from "./contentApi";

type Send = Parameters<typeof loadContent>[0];

function recorder(answer: (method: string, path: string) => unknown = () => ({})) {
  const calls: unknown[][] = [];
  const send = (async (...args: unknown[]) => {
    calls.push(args);
    const a = answer(args[0] as string, args[1] as string);
    if (a instanceof ApiError) throw a;
    return a;
  }) as NonNullable<Send>;
  return { send, calls };
}

describe("each button calls its route with the right body (dashboard.md section 3.2)", () => {
  it("run buttons", async () => {
    const { send, calls } = recorder();
    await contentApi.sync(send);
    await contentApi.stop(send);
    await contentApi.verify(send);
    await contentApi.importFrom("ark-3f9c0a12", send);
    expect(calls).toEqual([
      ["POST", "/ark/sync", { trigger: "manual" }],
      ["DELETE", "/ark/sync"],
      ["POST", "/ark/verify", {}],
      ["POST", "/ark/import", { from_root: "ark-3f9c0a12" }],
    ]);
  });
  it("drive buttons; init and prune carry the PIN grant", async () => {
    const { send, calls } = recorder();
    await contentApi.eject("ark-3f9c0a12", false, send);
    await contentApi.mount("ark-3f9c0a12", send);
    await contentApi.init("ark-3f9c0a12", "pg_x", send);
    await contentApi.prune(true, "pg_x", send);
    expect(calls).toEqual([
      ["POST", "/ark/drives/ark-3f9c0a12/eject", { force: false }],
      ["POST", "/ark/drives/ark-3f9c0a12/mount", {}],
      ["POST", "/ark/drives/ark-3f9c0a12/init", {}, "pg_x"],
      ["POST", "/ark/prune", { orphans: true, trash: true, dry_run: true }, "pg_x"],
    ]);
  });
});

describe("loadContent", () => {
  it("no run yet (404) is no run, not an error", async () => {
    const { send } = recorder((_, p) =>
      p === "/ark/sync" ? new ApiError(404, "LAI-AGT-021", "no run") : p === "/ark" ? { roots: [] } : { drives: [] },
    );
    expect(await loadContent(send)).toEqual({ roots: [], drives: [], run: null });
  });
  it("a failure of GET /ark is thrown with its code", async () => {
    const { send } = recorder((_, p) => (p === "/ark" ? new ApiError(500, "LAI-AGT-001", "boom") : { drives: [] }));
    await expect(loadContent(send)).rejects.toMatchObject({ code: "LAI-AGT-001" });
  });
});
