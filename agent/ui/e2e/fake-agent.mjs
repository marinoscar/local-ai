// A fake agent for the Playwright tests: it serves agent/ui/dist the way the real agent does
// (/portal SPA fallback, /portal/assets, the Portal CSP) and a status body the tests can change.
// Real agent behaviour is covered by the Go tests (agent/internal/api/spa_test.go); this only
// gives the browser something to talk to. Listens on 127.0.0.1 only.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { handleMaps } from "./fake-maps.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const dist = join(here, "..", "dist");
const port = Number(process.env.FAKE_AGENT_PORT || 7071);

// Keep in step with PortalContentSecurityPolicy in agent/internal/api/spa.go (survival-portal.md section 4.11).
export const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; worker-src 'self' blob:; frame-src 'self'; frame-ancestors 'self' localai://app; base-uri 'none'; form-action 'self'";

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript",
  ".css": "text/css",
  ".woff2": "font/woff2",
  ".json": "application/json",
};

function defaultStatus() {
  return {
    schema_version: 1,
    generated_at: "2026-11-02T14:05:11Z",
    node_id: "ai-n3",
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
    brain: {
      loaded: [
        {
          id: "qwen3.5-9b-q4km",
          display_name: "Qwen 3.5 9B",
          roles: ["chat"],
          vision: false,
        },
      ],
      loading: null,
      choices: [
        {
          id: "qwen3.5-9b-q4km",
          display_name: "Qwen 3.5 9B",
          summary: "Fast general assistant.",
          roles: ["chat"],
          vision: false,
          loadable: true,
          survival: true,
          speed_hint: "fast",
        },
        {
          id: "vlm-4b",
          display_name: "Vision 4B",
          summary: "Understands photos. A bit slower.",
          roles: ["vision"],
          vision: true,
          loadable: true,
          survival: true,
          speed_hint: "medium",
        },
        {
          id: "huge-70b",
          display_name: "Huge 70B",
          summary: "Does not fit.",
          roles: ["chat"],
          vision: false,
          loadable: false,
          survival: false,
          speed_hint: "slow",
        },
      ],
    },
    ark: { overall: "yellow", survival_capable: true, drive_connected: true },
    capabilities: {
      chat: { state: "ready" },
      photo: { state: "ready" },
      talk: { state: "ready" },
      library: { state: "ready" },
      maps: { state: "ready" },
      manuals: { state: "ready" },
    },
    first_aid: [
      {
        key: "cpr",
        title: "CPR",
        url: "/library/content/x/CPR",
        available: true,
      },
      {
        key: "choking",
        title: "Choking",
        url: "/library/content/x/Choking",
        available: true,
      },
      {
        key: "bleeding",
        title: "Severe bleeding",
        url: "/library/content/x/Bleeding",
        available: true,
      },
      {
        key: "burns",
        title: "Burns",
        url: "/library/content/x/Burns",
        available: true,
      },
      {
        key: "fracture",
        title: "Broken bone",
        url: "/library/content/x/Fracture",
        available: true,
      },
      {
        key: "snake",
        title: "Snake bite",
        url: "/library/content/x/Snake",
        available: true,
      },
    ],
    alerts: [],
  };
}

let status = defaultStatus();
let down = false;
let seen = [];
let streams = [];
let loadAnswer = 202;
// The answer of the fix buttons (POST /ark/import, /services/{kiwix|whisper}/restart): 2xx, 409 or 429.
let fixAnswer = 0;

function send(res, code, body, headers = {}) {
  res.writeHead(code, headers);
  res.end(body);
}

async function file(res, path, type, extra = {}) {
  try {
    send(res, 200, await readFile(path), {
      "Content-Type": type,
      "X-Content-Type-Options": "nosniff",
      ...extra,
    });
  } catch {
    send(res, 404, "not found");
  }
}

async function readBody(req) {
  let s = "";
  for await (const c of req) s += c;
  return s;
}

createServer(async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  const p = url.pathname;
  if (await handleMaps(req, res, url, CSP)) return;
  if (p.startsWith("/__")) {
    if (p === "/__reset") {
      status = defaultStatus();
      down = false;
      seen = [];
      loadAnswer = 202;
      fixAnswer = 0;
      return send(res, 200, "ok");
    }
    if (p === "/__status" && req.method === "POST") {
      status = { ...status, ...JSON.parse(await readBody(req)) };
      return send(res, 200, "ok");
    }
    if (p === "/__emit" && req.method === "POST") {
      // Push one event to every open /api/v1/events stream: {type, data}.
      const { type, data } = JSON.parse(await readBody(req));
      const frame = `event: ${type}\ndata: ${JSON.stringify({ type, data })}\n\n`;
      for (const r of streams) r.write(frame);
      return send(res, 200, "ok");
    }
    if (p === "/__load") {
      loadAnswer = Number(url.searchParams.get("status") || 202);
      return send(res, 200, "ok");
    }
    if (p === "/__fix") {
      fixAnswer = Number(url.searchParams.get("status") || 0);
      return send(res, 200, "ok");
    }
    if (p === "/__streams")
      return send(res, 200, String(streams.length));
    if (p === "/__down") {
      down = url.searchParams.get("on") === "1";
      return send(res, 200, "ok");
    }
    if (p === "/__seen")
      return send(res, 200, JSON.stringify(seen), {
        "Content-Type": "application/json",
      });
    return send(res, 404, "not found");
  }
  if (p === "/api/v1/portal/status") {
    seen.push({ path: p, lang: req.headers["x-localai-ui-language"] ?? null });
    if (down)
      return send(
        res,
        503,
        JSON.stringify({ error: { code: "LAI-AGT-001" } }),
        { "Content-Type": "application/json" },
      );
    return send(res, 200, JSON.stringify(status), {
      "Content-Type": "application/json",
    });
  }
  if (p === "/api/v1/mode/survival" && req.method === "POST") {
    status = { ...status, mode: "survival", session: "portal" };
    return send(res, 200, "{}", { "Content-Type": "application/json" });
  }
  if (/^\/api\/v1\/models\/[^/]+\/load$/.test(p) && req.method === "POST") {
    seen.push({ path: p, body: await readBody(req) });
    if (loadAnswer === 429)
      return send(
        res,
        429,
        JSON.stringify({
          error: {
            code: "LAI-SVC-114",
            message: "Too many switches",
            fix: "Wait one minute and try again.",
          },
        }),
        { "Content-Type": "application/json" },
      );
    return send(res, loadAnswer, "{}", { "Content-Type": "application/json" });
  }
  if (
    req.method === "POST" &&
    (p === "/api/v1/ark/import" ||
      /^\/api\/v1\/services\/(kiwix|whisper)\/restart$/.test(p))
  ) {
    seen.push({ path: p, body: await readBody(req) });
    const errors = {
      409: { code: "LAI-SVC-118", message: "No usable AI-ARK drive is connected to copy from", fix: "Plug in the AI-ARK drive, wait a minute, then press Copy from the AI-ARK drive again." },
      429: { code: "LAI-SVC-117", message: "Too many repair attempts from the Portal; wait a few minutes", fix: "Wait a few minutes, then press the button again." },
    };
    if (errors[fixAnswer])
      return send(res, fixAnswer, JSON.stringify({ error: errors[fixAnswer] }), { "Content-Type": "application/json" });
    const ok = p === "/api/v1/ark/import"
      ? [202, { run_id: "run-20261102T031500Z-b7c2", state: "running", events: "/api/v1/events?types=sync.*" }]
      : [200, { name: p.split("/")[4], active: "active", restored_models: [], op_id: "op_7k2m9q4r8t1c" }];
    return send(res, ok[0], JSON.stringify(ok[1]), { "Content-Type": "application/json" });
  }
  if (p === "/api/v1/events") {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
    });
    res.write(": open\n\n");
    streams.push(res);
    req.on("close", () => {
      streams = streams.filter((r) => r !== res);
    });
    return;
  }
  if (
    p === "/portal" ||
    p === "/portal/" ||
    (p.startsWith("/portal/") && !p.startsWith("/portal/assets/"))
  ) {
    return file(res, join(dist, "portal.html"), TYPES[".html"], {
      "Content-Security-Policy": CSP,
    });
  }
  if (p.startsWith("/portal/assets/")) {
    const rel = normalize(p.slice("/portal/".length));
    if (rel.includes("..")) return send(res, 400, "bad");
    return file(
      res,
      join(dist, rel),
      TYPES[extname(rel)] ?? "application/octet-stream",
    );
  }
  if (p === "/") {
    if (status.mode === "survival")
      return send(res, 302, "", { Location: "/portal" });
    return file(res, join(dist, "index.html"), TYPES[".html"]);
  }
  // kiwix-serve search: OpenSearch RSS, or its HTML page when format is not xml.
  if (p === "/library/search") {
    seen.push({
      path: p + url.search,
      lang: req.headers["x-localai-ui-language"] ?? null,
    });
    if (url.searchParams.get("format") !== "xml")
      return send(res, 200, "<!doctype html><title>results</title><p>results</p>", {
        "Content-Type": TYPES[".html"],
        "Content-Security-Policy": CSP,
      });
    const rss = `<?xml version="1.0"?><rss version="2.0"><channel><totalResults>2</totalResults>
<item><title>Water purification</title><link>/library/content/wikipedia_en_all/Water_purification</link><description>Boil it</description><book><title>Wikipedia</title></book></item>
<item><title>Boiling water</title><link>/library/content/survival/Boiling</link><description>Rolling boil</description><book><title>Survival library</title></book></item>
</channel></rss>`;
    if (url.searchParams.get("pattern") === "nothing")
      return send(
        res,
        200,
        "<rss><channel><totalResults>0</totalResults></channel></rss>",
        { "Content-Type": "application/rss+xml" },
      );
    return send(res, 200, rss, { "Content-Type": "application/rss+xml" });
  }
  // Other areas of the real agent (chat, library, docs): a page is enough.
  if (/^\/(chat|library|maps|docs)(\/|$)/.test(p)) {
    return send(res, 200, `<!doctype html><title>${p}</title><p>${p}</p>`, {
      "Content-Type": TYPES[".html"],
      "Content-Security-Policy": CSP,
    });
  }
  send(res, 404, "not found");
}).listen(port, "127.0.0.1", () =>
  console.log(`fake agent on http://127.0.0.1:${port}`),
);
