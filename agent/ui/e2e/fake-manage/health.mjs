// Fake agent routes of the Health screen (dashboard.md section 4.5, see e2e/fake-manage.mjs):
// GET /doctor, GET /logs, POST/GET/DELETE /drill. Shapes are agent.md sections 3.8.23, 3.8.27 and
// 3.8.28 and drill.md section 3.4. The test control is POST /__health with any of:
//   doctor: "problems" | "ok" | "error"   what GET /doctor answers (default "problems")
//   doctorDelayMs: number                  how long GET /doctor takes (default 0)
//   logs: "some" | "none" | "error"        what GET /logs answers (default "some")
//   drill: "none" | "last"                 the last results document of GET /drill (default "none")
//   drillPolls: number                     how many GET ?run_id= a run stays running (default 2;
//                                          a large number keeps it running, for Stop)
//   drillStart: 0 | 409                    409 answers LAI-DRL-005 (low battery)

const RAN_AT = "2026-11-14T12:00:00Z";
const PASSED = ["agt.config", "hw.disk", "llm.router_health", "net.loopback_only", "svc.whisper"];

const PROBLEMS = {
  ran_at: RAN_AT,
  duration_ms: 2310,
  summary: { ok: PASSED.length, info: 1, warning: 1, error: 0, critical: 0 },
  results: [
    {
      check: "svc.kiwix",
      code: "LAI-SVC-002",
      severity: "warning",
      title: "The library is not answering",
      fix: "Restart the library: `aictl services restart kiwix`.",
      action: "service.restart:kiwix",
      details: { state: "failed" },
    },
    {
      check: "agt.upgrade",
      code: "LAI-AGT-015",
      severity: "info",
      title: "A newer Local AI is available",
      fix: "Update when you have the internet.",
    },
  ],
  passed: PASSED,
};

const ALL_OK = {
  ran_at: RAN_AT,
  duration_ms: 1900,
  summary: { ok: PASSED.length, info: 0, warning: 0, error: 0, critical: 0 },
  results: [],
  passed: PASSED,
};

const LOGS = {
  unit: "localai-agent.service",
  truncated: false,
  lines: [
    { at: "2026-11-14T11:58:00Z", priority: 6, message: '{"level":"INFO","msg":"the agent started","component":"app"}' },
    {
      at: "2026-11-14T11:59:10Z",
      priority: 6,
      message: '{"level":"WARN","msg":"the library is not answering","code":"LAI-SVC-002","component":"services"}',
    },
    { at: "2026-11-14T11:59:30Z", priority: 3, message: "kiwix-serve exited with status 1" },
  ],
};

function checksDone(n) {
  const all = [
    { id: "drl.fence", area: "net", result: "pass", ms: 40 },
    { id: "drl.config.offline_lint", area: "config", result: "pass", ms: 120 },
    { id: "drl.stt.es", area: "speech", result: "warn", code: "LAI-DRL-031", ms: 2900 },
    { id: "drl.kiwix.search", area: "library", result: "fail", code: "LAI-DRL-030", ms: 800 },
    { id: "drl.restore", area: "restore", result: "pass", ms: 10 },
  ];
  return all.slice(0, n);
}

function drillDoc(runId, extra) {
  return {
    schema_version: 1,
    run_id: runId,
    trigger: "manual",
    node_id: "ai-n3",
    agent_version: "0.4.0",
    catalog_version: "2026.11.02-1",
    mode: "online",
    quick: true,
    started_at: "2026-11-14T12:00:00Z",
    finished_at: null,
    overall: "running",
    survival_ready: false,
    counts: { pass: 0, warn: 0, fail: 0, skip: 0 },
    checks: [],
    network_fence: { enabled: true, blocked_dials: 0 },
    ...extra,
  };
}

const LAST = drillDoc("drl-20261101T043512Z-9c1e", {
  trigger: "scheduled",
  quick: false,
  started_at: "2026-11-01T04:35:12Z",
  finished_at: "2026-11-01T05:21:40Z",
  overall: "pass",
  survival_ready: true,
  counts: { pass: 27, warn: 0, fail: 0, skip: 1 },
  checks: [{ id: "drl.fence", area: "net", result: "pass", ms: 40 }],
  restored: { loaded_models: [], default_model: "", ok: true },
});

let opts;
let run; // {id, polls, aborted, done}
let last;

export function reset() {
  opts = { doctor: "problems", doctorDelayMs: 0, logs: "some", drill: "none", drillPolls: 2, drillStart: 0 };
  run = null;
  last = null;
}
reset();

function finished(r) {
  if (r.aborted)
    return drillDoc(r.id, {
      state: "aborted",
      overall: "aborted",
      reason: "the drill was stopped",
      finished_at: "2026-11-14T12:01:00Z",
      checks: checksDone(1),
      restored: { loaded_models: [], default_model: "", ok: true },
    });
  return drillDoc(r.id, {
    overall: "fail",
    survival_ready: false,
    finished_at: "2026-11-14T12:02:10Z",
    counts: { pass: 3, warn: 1, fail: 1, skip: 0 },
    checks: checksDone(5),
    restored: { loaded_models: [], default_model: "", ok: true },
  });
}

function runningDoc(r) {
  return drillDoc(r.id, { state: "running", checks: checksDone(Math.min(r.polls + 1, 4)) });
}

const sleep = (ms) => new Promise((ok) => setTimeout(ok, ms));

export async function handle(req, res, url, ctx) {
  const p = url.pathname;
  if (p === "/__health" && req.method === "POST") {
    opts = { ...opts, ...JSON.parse(await ctx.readBody(req)) };
    ctx.json(res, 200, { ok: true });
    return true;
  }
  if (p === "/api/v1/doctor" && req.method === "GET") {
    ctx.record({ path: p, method: "GET", query: url.search });
    if (opts.doctorDelayMs) await sleep(opts.doctorDelayMs);
    if (opts.doctor === "error")
      ctx.json(res, 503, {
        error: { code: "LAI-AGT-024", message: "The agent is stopping", fix: "Wait a minute and try again." },
      });
    else ctx.json(res, 200, opts.doctor === "ok" ? ALL_OK : PROBLEMS);
    return true;
  }
  if (p === "/api/v1/logs" && req.method === "GET") {
    ctx.record({ path: p, method: "GET", query: url.search });
    if (opts.logs === "error")
      ctx.json(res, 504, {
        error: { code: "LAI-AGT-099", message: "Reading the events took too long", fix: "Try again in a minute." },
      });
    else if (opts.logs === "none") ctx.json(res, 200, { unit: "localai-agent.service", lines: [], truncated: false });
    else ctx.json(res, 200, LOGS);
    return true;
  }
  if (p === "/api/v1/drill") {
    if (req.method === "POST") {
      const body = await ctx.readBody(req);
      ctx.record({ path: p, method: "POST", body });
      if (opts.drillStart === 409) {
        ctx.json(res, 409, {
          error: {
            code: "LAI-DRL-005",
            message: "The battery is too low for a drill",
            fix: "Plug in the charger, then start the drill again.",
          },
        });
        return true;
      }
      run = { id: "drl-20261114T120000Z-a1b2", polls: 0, aborted: false, done: false };
      ctx.json(res, 202, { run_id: run.id });
      return true;
    }
    if (req.method === "DELETE") {
      ctx.record({ path: p, method: "DELETE" });
      if (!run || run.done) {
        ctx.json(res, 404, { error: { code: "LAI-AGT-021", message: "No drill is running", fix: "" } });
        return true;
      }
      run.aborted = true;
      ctx.json(res, 202, { run_id: run.id });
      return true;
    }
    if (req.method === "GET") {
      const id = url.searchParams.get("run_id");
      ctx.record({ path: p, method: "GET", query: url.search });
      if (id) {
        if (!run || run.id !== id) {
          ctx.json(res, 404, { error: { code: "LAI-AGT-021", message: "That drill is no longer kept", fix: "" } });
          return true;
        }
        if (run.aborted || run.polls >= opts.drillPolls) {
          run.done = true;
          last = finished(run);
          ctx.json(res, 200, last);
        } else {
          ctx.json(res, 200, runningDoc(run));
          run.polls++;
        }
        return true;
      }
      const doc = last ?? (opts.drill === "last" ? LAST : null);
      if (!doc) ctx.json(res, 404, { error: { code: "LAI-AGT-021", message: "No drill has run yet", fix: "" } });
      else ctx.json(res, 200, doc);
      return true;
    }
  }
  return false;
}
