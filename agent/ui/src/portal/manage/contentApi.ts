import { ApiError, apiSend } from "../api";
import type { Drive, PruneResult, ReadyReport, SyncRun } from "./content";

// The calls of the Offline content and drives screen (dashboard.md section 3.2, agent.md sections
// 3.8.17 to 3.8.20a). Each takes `send` so the route table is unit-tested (contentApi.test.ts).

type Send = typeof apiSend;

/** The bodies the agent answers when a run is accepted (sync, verify, import). */
export interface RunStarted {
  run_id: string;
  state: string;
  reason?: string;
  code?: string;
}

const id = (rootId: string) => encodeURIComponent(rootId);

export const contentApi = {
  /** POST /ark/sync: a manual sync of every writable root. */
  sync: (send: Send = apiSend) => send<RunStarted>("POST", "/ark/sync", { trigger: "manual" }),
  /** DELETE /ark/sync: stop the running sync, check or copy (graceful, resumable). */
  stop: (send: Send = apiSend) => send<RunStarted>("DELETE", "/ark/sync"),
  /** POST /ark/verify: re-read the copies on every root that can be read. */
  verify: (send: Send = apiSend) => send<RunStarted>("POST", "/ark/verify", {}),
  /** POST /ark/import: copy the plan's items from one drive (never deletes anything). */
  importFrom: (rootId: string, send: Send = apiSend) =>
    send<RunStarted>("POST", "/ark/import", { from_root: rootId }),
  /** POST /ark/drives/{root_id}/eject; force unloads a brain whose only copy is on the drive. */
  eject: (rootId: string, force: boolean, send: Send = apiSend) =>
    send<{ state: string }>("POST", `/ark/drives/${id(rootId)}/eject`, { force }),
  /** POST /ark/drives/{root_id}/mount: open a connected drive that is not mounted. */
  mount: (rootId: string, send: Send = apiSend) => send<Drive>("POST", `/ark/drives/${id(rootId)}/mount`, {}),
  /** POST /ark/drives/{root_id}/init: set up an empty AI-ARK drive (PIN). */
  init: (rootId: string, grant: string, send: Send = apiSend) =>
    send<Drive>("POST", `/ark/drives/${id(rootId)}/init`, {}, grant),
  /** POST /ark/prune: content not in the plan and old copies already replaced (PIN). */
  prune: (dryRun: boolean, grant: string, send: Send = apiSend) =>
    send<PruneResult>("POST", "/ark/prune", { orphans: true, trash: true, dry_run: dryRun }, grant),
};

/** What the screen shows: every root's report, the drives and the active or last run. */
export interface ContentData {
  roots: ReadyReport[];
  drives: Drive[];
  run: SyncRun | null;
}

/**
 * loadContent reads GET /ark, GET /ark/drives and GET /ark/sync. No run yet (404) or an engine that
 * is not ready (503) is no run, not an error; a failure of the other two is thrown.
 */
export async function loadContent(send: Send = apiSend): Promise<ContentData> {
  const [ark, drives, run] = await Promise.all([
    send<{ roots: ReadyReport[] }>("GET", "/ark"),
    send<{ drives: Drive[] }>("GET", "/ark/drives"),
    send<SyncRun>("GET", "/ark/sync").catch((e: unknown) => {
      if (e instanceof ApiError && (e.status === 404 || e.status === 503)) return null;
      throw e;
    }),
  ]);
  return { roots: ark?.roots ?? [], drives: drives?.drives ?? [], run: run ?? null };
}
