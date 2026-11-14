// Fake agent routes of the System screen (dashboard.md section 4.6, issue #500): GET /api/v1/status
// (agent.md 3.8.4, only the fields the screen reads plus a few real ones), GET /api/v1/catalog (3.8.21),
// POST /api/v1/catalog/refresh and POST /api/v1/services/llama/restart (3.8.22). kiwix and whisper
// restarts are the shared fix-button route of fake-agent.mjs.
// POST /__system?restart=502 makes the llama restart fail; ?refresh=409 makes the refresh fail.
let restartAnswer = 200;
let refreshAnswer = 200;

export function reset() {
  restartAnswer = 200;
  refreshAnswer = 200;
}

function agentStatus() {
  return {
    node: {
      id: "ai-n3",
      display_name: "Test node",
      profile: "small-8gb",
      agent_version: "0.1.0-rc.7",
      backend: "cuda",
      llama_mode: "router",
      llama_build: null,
      catalog_version: "2026.11.02-1",
      uptime_s: 100,
      time: "2026-11-14T12:00:00Z",
    },
    mode: { value: "online", since: null, transition: null },
    services: [
      { name: "llama", unit: "localai-llama.service", active: "active", sub: "running", since: null, restarts: 0, healthy: true, state: "ok" },
      { name: "kiwix", unit: "localai-kiwix.service", active: "inactive", sub: "dead", since: null, restarts: 0, healthy: false, state: "down" },
      { name: "whisper", unit: "localai-whisper.service", active: "active", sub: "running", since: null, restarts: 0, healthy: true, state: "degraded" },
      { name: "piper", unit: null, state: "ok" },
    ],
    alerts: [],
  };
}

function catalog() {
  const kit = (id, verified) => ({ id, kind: "kit", display_name: id, present: verified, verified, in_profile: true, size_bytes: 1, status: verified ? "verified" : "missing" });
  return {
    version: "2026.11.02-1",
    source: "offline",
    fetched_at: null,
    models: [],
    content: [kit("kit-agent", true), kit("kit-llama-src", true), kit("kit-apt", false), { ...kit("wikipedia", true), kind: "kiwix" }],
    profiles: [],
  };
}

export async function handle(req, res, url, ctx) {
  const p = url.pathname;
  if (p === "/__system") {
    restartAnswer = Number(url.searchParams.get("restart") || restartAnswer);
    refreshAnswer = Number(url.searchParams.get("refresh") || refreshAnswer);
    ctx.json(res, 200, {});
    return true;
  }
  if (p === "/api/v1/status" && req.method === "GET") {
    ctx.record({ path: p, method: "GET" });
    ctx.json(res, 200, agentStatus());
    return true;
  }
  if (p === "/api/v1/catalog" && req.method === "GET") {
    ctx.record({ path: p, method: "GET" });
    ctx.json(res, 200, catalog());
    return true;
  }
  if (p === "/api/v1/catalog/refresh" && req.method === "POST") {
    ctx.record({ path: p, method: "POST", body: await ctx.readBody(req) });
    if (refreshAnswer === 409) {
      ctx.json(res, 409, { error: { code: "LAI-AGT-023", message: "Refreshing the catalog needs the internet", fix: "Switch to online mode first." } });
      return true;
    }
    ctx.json(res, 200, { version: "2026.11.14-1", previous: "2026.11.02-1", changed: true, source: "github" });
    return true;
  }
  if (p === "/api/v1/services/llama/restart" && req.method === "POST") {
    ctx.record({ path: p, method: "POST", body: await ctx.readBody(req) });
    if (restartAnswer === 502) {
      ctx.json(res, 502, { error: { code: "LAI-LLM-001", message: "The AI brain is not running", fix: "Press Restart again; if it stays stopped, run a full check." } });
      return true;
    }
    ctx.json(res, 200, { name: "llama", active: "active", restored_models: ["qwen3.5-9b-q4km"], op_id: "op_1" });
    return true;
  }
  return false;
}
