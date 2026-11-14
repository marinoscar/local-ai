// Fake agent routes of the Offline content and drives screen (dashboard.md section 4.3, issue #496;
// shapes of ark-sync.md section 3.5). GET /ark, GET /ark/drives, /ark/sync (POST/GET/DELETE),
// POST /ark/verify, POST /ark/import (operator session only; the Portal's fix button keeps the shared
// handler of fake-agent.mjs), drives eject/init/mount, POST /ark/prune.
// Test control: POST /__content?verify=<status> makes the next POST /ark/verify answer that error.
const GRANT_HEADER = "x-localai-pin-grant";
const RUNNING = "run-20261114T120000Z-c0de";

function internalRoot() {
  return {
    schema_version: 1,
    generated_at: "2026-11-14T04:02:10Z",
    root: { id: "internal", kind: "internal", read_only: false, fs_total_bytes: 502000000000, fs_free_bytes: 105300000000 },
    overall: "red",
    survival_capable: true,
    clock_wrong: false,
    last_sync: { run_id: "run-20261114T031500Z-a1f3", trigger: "scheduled", finished_at: "2026-11-14T04:02:09Z", state: "done", age_days: 0 },
    counts: { verified: 3, stale: 0, missing: 1, skipped: 0, failed: 0, quarantined: 1, orphan: 1 },
    items: [
      { id: "osm-north-america", kind: "maps", tier: 1, status: "verified", bytes: 11200000000 },
      { id: "wikipedia-en-maxi", kind: "kiwix", tier: 1, status: "missing", reason: "not downloaded yet", bytes: 110000000000 },
      { id: "wikem-en", kind: "kiwix", tier: 1, status: "quarantined", reason: "checksum mismatch", code: "LAI-SYN-020", bytes: 42000000 },
      { id: "qwen3.5-9b-q4km", kind: "model", tier: 1, survival: true, status: "verified", bytes: 5870000000 },
      { id: "whisper-small", kind: "model", tier: 1, status: "verified", bytes: 487000000 },
      { id: "gutenberg-en", kind: "kiwix", tier: 3, status: "orphan", bytes: 7000000000 },
    ],
    problems: [
      { code: "LAI-SYN-020", severity: "error", item: "wikem-en", title: "A download was damaged and set aside." },
    ],
  };
}

function drivesList() {
  return [
    {
      root_id: "ark-3f9c0a12",
      device: "/dev/sdb1",
      mount: "/media/operator/AI-ARK",
      fstype: "ext4",
      label: "AI-ARK",
      size_bytes: 2000398934016,
      free_bytes: 812345678901,
      state: "ready",
      read_only: false,
      fs_errors: 0,
      initialized: true,
      profile: "ark-drive-full",
      ready: { overall: "green", generated_at: "2026-11-14T04:02:10Z" },
      problems: [],
    },
    {
      root_id: "ark-77aa0011",
      device: "/dev/sdc1",
      mount: "/media/operator/AI-ARK1",
      fstype: "ext4",
      label: "AI-ARK",
      size_bytes: 1000204886016,
      free_bytes: 1000000000000,
      state: "uninitialized",
      read_only: false,
      initialized: false,
      ready: null,
      problems: [],
    },
  ];
}

function lastRun() {
  return {
    run_id: "run-20261114T031500Z-a1f3",
    trigger: "scheduled",
    state: "done",
    started_at: "2026-11-14T03:15:02Z",
    finished_at: "2026-11-14T04:02:09Z",
    roots: ["internal"],
    totals: { items: 6, done: 4, skipped: 1, failed: 1, bytes_total: 0, bytes_done: 0, rate_bps: 0, eta_s: 0 },
    active: [],
    result: "partial",
    items: [{ root: "internal", item: "wikem-en", result: "skipped", reason: "blocked", code: "LAI-SYN-020" }],
  };
}

function runningRun(trigger) {
  return {
    run_id: RUNNING,
    trigger,
    state: "running",
    started_at: "2026-11-14T12:00:00Z",
    finished_at: null,
    roots: ["internal", "ark-3f9c0a12"],
    totals: { items: 6, done: 2, skipped: 0, failed: 0, bytes_total: 120000000000, bytes_done: 30000000000, rate_bps: 18874368, eta_s: 4800 },
    active: [
      { root: "internal", item: "wikipedia-en-maxi", state: "downloading", version: "2026-10", bytes_done: 30000000000, bytes_total: 110000000000, rate_bps: 18874368, attempt: 1 },
    ],
  };
}

let drives = drivesList();
let run = lastRun();
// GETs of /ark/drives left before an ejecting drive leaves the list (it was unmounted and powered off).
let ejectTicks = {};
let verifyAnswer = 0;

export function reset() {
  drives = drivesList();
  run = lastRun();
  ejectTicks = {};
  verifyAnswer = 0;
}

function hasGrant(req) {
  return req.headers[GRANT_HEADER] === "pg_fake";
}

function pinRequired(ctx, res) {
  ctx.json(res, 403, {
    error: { code: "LAI-SEC-002", message: "This needs the admin PIN", fix: "Enter the admin PIN.", details: { pin_required: true } },
  });
}

export async function handle(req, res, url, ctx) {
  const p = url.pathname;
  if (p === "/__content") {
    verifyAnswer = Number(url.searchParams.get("verify") || 0);
    ctx.json(res, 200, {});
    return true;
  }
  if (!p.startsWith("/api/v1/ark")) return false;
  const m = req.method;

  if (p === "/api/v1/ark" && m === "GET") {
    ctx.json(res, 200, { roots: [internalRoot()] });
    return true;
  }
  if (p === "/api/v1/ark/drives" && m === "GET") {
    for (const id of Object.keys(ejectTicks)) {
      ejectTicks[id] -= 1;
      if (ejectTicks[id] <= 0) {
        drives = drives.filter((d) => d.root_id !== id);
        delete ejectTicks[id];
      }
    }
    ctx.json(res, 200, { drives });
    return true;
  }
  if (p === "/api/v1/ark/sync") {
    if (m === "GET") {
      ctx.json(res, 200, run);
      return true;
    }
    if (m === "DELETE") {
      ctx.record({ path: p, method: m });
      const wasRunning = run.state === "running";
      if (wasRunning)
        run = { ...run, state: "cancelled", result: "cancelled", finished_at: "2026-11-14T12:05:00Z", active: [], items: [] };
      ctx.json(res, 202, { run_id: run.run_id, state: wasRunning ? "cancelling" : "idle" });
      return true;
    }
    if (m === "POST") {
      const body = await ctx.readBody(req);
      ctx.record({ path: p, method: m, body });
      const mode = ctx.getStatus().mode;
      if (mode === "survival") {
        ctx.json(res, 409, { error: { code: "LAI-SYN-010", message: "Sync is off in survival mode", fix: "Leave survival mode first." } });
        return true;
      }
      if (mode !== "online") {
        ctx.json(res, 409, { error: { code: "LAI-SYN-011", message: "This computer is offline", fix: "Switch to online mode first." } });
        return true;
      }
      run = runningRun("manual");
      ctx.json(res, 202, { run_id: RUNNING, state: "running", events: "/api/v1/events?types=sync.*" });
      return true;
    }
  }
  if (p === "/api/v1/ark/verify" && m === "POST") {
    const body = await ctx.readBody(req);
    ctx.record({ path: p, method: m, body });
    if (verifyAnswer) {
      ctx.json(res, verifyAnswer, {
        error: { code: "LAI-SYN-005", message: "Another sync or check is already running", fix: "Wait for it to finish, or press Stop." },
      });
      return true;
    }
    run = runningRun("verify");
    ctx.json(res, 202, { run_id: RUNNING, state: "running", events: "/api/v1/events?types=sync.*" });
    return true;
  }
  if (p === "/api/v1/ark/import" && m === "POST") {
    // The Portal session's fix button keeps the shared handler of fake-agent.mjs.
    if (ctx.getStatus().session !== "full") return false;
    const body = await ctx.readBody(req);
    ctx.record({ path: p, method: m, body });
    run = runningRun("import");
    ctx.json(res, 202, { run_id: RUNNING, state: "running", events: "/api/v1/events?types=sync.*" });
    return true;
  }
  const dm = /^\/api\/v1\/ark\/drives\/([^/]+)\/(eject|init|mount)$/.exec(p);
  if (dm && m === "POST") {
    const [, id, action] = dm;
    const body = await ctx.readBody(req);
    ctx.record({ path: p, method: m, body, grant: req.headers[GRANT_HEADER] ?? "" });
    const d = drives.find((x) => x.root_id === id);
    if (!d) {
      ctx.json(res, 404, { error: { code: "LAI-AGT-021", message: "No such drive" } });
      return true;
    }
    if (action === "eject") {
      d.state = "ejecting";
      ejectTicks[id] = 2;
      ctx.json(res, 202, { state: "ejecting" });
      return true;
    }
    if (action === "init") {
      if (!hasGrant(req)) {
        pinRequired(ctx, res);
        return true;
      }
      Object.assign(d, { state: "ready", initialized: true, profile: "ark-drive-full", ready: { overall: "red", generated_at: "2026-11-14T12:00:00Z" } });
      ctx.json(res, 200, d);
      return true;
    }
    Object.assign(d, { state: "ready" });
    ctx.json(res, 200, d);
    return true;
  }
  if (p === "/api/v1/ark/prune" && m === "POST") {
    const body = await ctx.readBody(req);
    ctx.record({ path: p, method: m, body, grant: req.headers[GRANT_HEADER] ?? "" });
    if (!hasGrant(req)) {
      pinRequired(ctx, res);
      return true;
    }
    ctx.json(res, 200, {
      deleted: [{ root: "internal", item: "gutenberg-en", version: "20260501", bytes: 7000000000, kind: "orphan" }],
      refused: [],
      freed_bytes: JSON.parse(body || "{}").dry_run ? 0 : 7000000000,
    });
    return true;
  }
  return false;
}
