// The AI models screen's fake routes (dashboard.md section 4.4; agent.md sections 3.8.7 to 3.8.12):
// GET /api/v1/models, unload, PUT /models/default, download (with GET /api/v1/ark/sync progress) and
// DELETE (admin: needs X-LocalAI-Pin-Grant). POST /models/{id}/load only moves the model to loading
// here and falls through to fake-agent.mjs, which records it and answers (its /__load control).
// Test control: POST /__models?fail=<op> makes the next <op> (unload, default, download, delete)
// answer its error.
const GB = 1_000_000_000;

function model(p) {
  return {
    summary: "",
    roles: ["chat"],
    in_profile: true,
    present: true,
    verified: true,
    locations: [{ root: "/srv/localai", complete: true }],
    loadable: true,
    loadable_reason: null,
    status: "stored",
    loaded: false,
    default: false,
    aliases: [],
    requirements: { backends: ["cpu"], min_vram_mib: 0, min_ram_mib: 0 },
    ...p,
  };
}

function initial() {
  return [
    model({ id: "qwen3.5-9b-q4km", display_name: "Qwen 3.5 9B", engine: "llama", summary: "Fast general assistant.", size_bytes: 5_868_826_976, status: "loaded", loaded: true, default: true }),
    model({ id: "vlm-4b", display_name: "Vision 4B", engine: "llama", summary: "Understands photos.", size_bytes: 3.2 * GB }),
    model({ id: "big-27b", display_name: "Big 27B", engine: "llama", summary: "Careful answers. Slower.", size_bytes: 16.5 * GB, present: false, verified: false, locations: [], status: "absent", loadable: false, loadable_reason: { code: "LAI-LLM-008", message: "The model files are missing." } }),
    model({ id: "huge-70b", display_name: "Huge 70B", engine: "llama", size_bytes: 42 * GB, present: false, verified: false, locations: [], status: "absent", loadable: false, loadable_reason: { code: "LAI-CAT-005", message: "Needs 44.0 GB of graphics memory; this computer has 8.0 GB." } }),
    model({ id: "extra-14b", display_name: "Extra 14B", engine: "llama", in_profile: false, size_bytes: 9 * GB, present: false, verified: false, locations: [], status: "absent", loadable: false, loadable_reason: { code: "LAI-LLM-008", message: "The model files are missing." } }),
    model({ id: "whisper-small", display_name: "Whisper small", engine: "whisper", roles: ["stt"], size_bytes: 466_000_000, status: "n/a" }),
  ];
}

let models = initial();
let fail = "";
let run = null; // {run_id, item, polls}

export function reset() {
  models = initial();
  fail = "";
  run = null;
}

const ERRORS = {
  unload: [409, { code: "LAI-AGT-023", message: "This action is not allowed in the current mode", fix: "Start another brain first, then unload this one." }],
  default: [422, { code: "LAI-CAT-005", message: "This model does not fit this computer", fix: "Choose a smaller model." }],
  download: [409, { code: "LAI-SYN-005", message: "A copy or check is running", fix: "Try again when it ends." }],
  delete: [409, { code: "LAI-AGT-022", message: "Another operation is already running", fix: "Wait for it to finish, then try again." }],
};

function failing(op, res, ctx) {
  if (fail !== op) return false;
  fail = "";
  const [code, error] = ERRORS[op];
  ctx.json(res, code, { error });
  return true;
}

const find = (id) => models.find((m) => m.id === decodeURIComponent(id));

function list() {
  // A model loading becomes loaded at the next read (the router watcher's next poll).
  const out = JSON.parse(JSON.stringify(models));
  for (const m of models)
    if (m.status === "loading") {
      m.status = "loaded";
      m.loaded = true;
    }
  const def = models.find((m) => m.default);
  return { models_max: 1, default: def ? def.id : null, models: out };
}

export async function handle(req, res, url, ctx) {
  const p = url.pathname;
  if (p === "/__models") {
    fail = url.searchParams.get("fail") || "";
    ctx.json(res, 200, {});
    return true;
  }
  if (p === "/api/v1/models" && req.method === "GET") {
    ctx.json(res, 200, list());
    return true;
  }
  let m;
  if ((m = p.match(/^\/api\/v1\/models\/([^/]+)\/load$/)) && req.method === "POST") {
    const row = find(m[1]);
    if (row) {
      for (const o of models)
        if (o.engine === "llama" && o.loaded) {
          o.status = "stored";
          o.loaded = false;
        }
      row.status = "loading";
    }
    return false; // fake-agent.mjs records the body and answers
  }
  if ((m = p.match(/^\/api\/v1\/models\/([^/]+)\/unload$/)) && req.method === "POST") {
    ctx.record({ path: p, method: "POST", body: await ctx.readBody(req) });
    if (failing("unload", res, ctx)) return true;
    const row = find(m[1]);
    row.status = "stored";
    row.loaded = false;
    ctx.json(res, 200, { id: row.id, status: "unloaded", already_unloaded: false, op_id: "op_fake" });
    return true;
  }
  if (p === "/api/v1/models/default" && req.method === "PUT") {
    const body = await ctx.readBody(req);
    ctx.record({ path: p, method: "PUT", body });
    if (failing("default", res, ctx)) return true;
    const { id } = JSON.parse(body || "{}");
    const prev = models.find((x) => x.default);
    for (const o of models) o.default = o.id === id;
    ctx.json(res, 200, { default: id, previous: prev ? prev.id : null, ini_regenerated: true });
    return true;
  }
  if ((m = p.match(/^\/api\/v1\/models\/([^/]+)\/download$/)) && req.method === "POST") {
    ctx.record({ path: p, method: "POST", body: await ctx.readBody(req) });
    if (failing("download", res, ctx)) return true;
    const mode = ctx.getStatus().mode;
    if (mode === "survival") {
      ctx.json(res, 409, { error: { code: "LAI-SYN-010", message: "No downloads in survival mode" } });
      return true;
    }
    const row = find(m[1]);
    run = { run_id: "run-20261114T120000Z-a1f3", item: row.id, polls: 0 };
    ctx.json(res, 202, { id: row.id, sync_run_id: run.run_id, queued: true });
    return true;
  }
  if (p === "/api/v1/ark/sync" && req.method === "GET" && run) {
    run.polls += 1;
    const row = models.find((x) => x.id === run.item);
    if (run.polls < 3) {
      const done = Math.round((row.size_bytes * run.polls) / 4);
      ctx.json(res, 200, {
        run_id: run.run_id, trigger: "manual", state: "running",
        totals: { items: 1, done: 0, skipped: 0, failed: 0, bytes_total: row.size_bytes, bytes_done: done, rate_bps: 1, eta_s: 1 },
        active: [{ root: "internal", item: row.id, state: "downloading", bytes_done: done, bytes_total: row.size_bytes, rate_bps: 1, attempt: 1 }],
      });
      return true;
    }
    Object.assign(row, { present: true, verified: true, status: "stored", locations: [{ root: "/srv/localai", complete: true }], loadable: true, loadable_reason: null });
    ctx.json(res, 200, { run_id: run.run_id, trigger: "manual", state: "done", result: "ok", items: [{ root: "internal", item: row.id, result: "ok" }] });
    return true;
  }
  if ((m = p.match(/^\/api\/v1\/models\/([^/]+)$/)) && req.method === "DELETE") {
    const grant = req.headers["x-localai-pin-grant"] ?? null;
    ctx.record({ path: p, method: "DELETE", query: url.search, grant });
    if (!grant) {
      ctx.json(res, 403, { error: { code: "LAI-SEC-002", message: "This needs the admin PIN", details: { pin_required: true } } });
      return true;
    }
    if (failing("delete", res, ctx)) return true;
    const row = find(m[1]);
    if (row.loaded) {
      ctx.json(res, 409, { error: { code: "LAI-AGT-022", message: "Unload it first", details: { reason: "loaded" } } });
      return true;
    }
    if (url.searchParams.get("keep_last") !== "false" && row.locations.length < 2) {
      ctx.json(res, 409, { error: { code: "LAI-SYN-044", message: "The last verified copy was kept; nothing was deleted", details: { kept: [{ root: "internal", reason: "last_verified_copy" }] } } });
      return true;
    }
    Object.assign(row, { present: false, verified: false, status: row.engine === "llama" ? "absent" : "n/a", locations: [] });
    ctx.json(res, 200, { id: row.id, deleted: [{ root: "internal", bytes: row.size_bytes }], op_id: "op_fake" });
    return true;
  }
  return false;
}
