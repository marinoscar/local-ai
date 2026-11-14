// POST /api/v1/pin/verify (agent.md section 3.8.3). The PIN is 4821; a wrong one counts down from 5.
// POST /__pin?state=unset makes it answer 409 LAI-SEC-005.
let remaining = 5;
let unset = false;

export function reset() {
  remaining = 5;
  unset = false;
}

export async function handle(req, res, url, ctx) {
  if (url.pathname === "/__pin") {
    unset = url.searchParams.get("state") === "unset";
    ctx.json(res, 200, {});
    return true;
  }
  if (url.pathname !== "/api/v1/pin/verify" || req.method !== "POST") return false;
  const { pin } = JSON.parse((await ctx.readBody(req)) || "{}");
  ctx.record({ path: url.pathname, method: "POST" });
  if (unset) {
    ctx.json(res, 409, { error: { code: "LAI-SEC-005", message: "No admin PIN is set", fix: "Set one with sudo aictl pin set." } });
    return true;
  }
  if (pin !== "4821") {
    remaining -= 1;
    ctx.json(res, 401, { error: { code: "LAI-SEC-003", message: "Wrong PIN", details: { remaining_attempts: remaining } } });
    return true;
  }
  ctx.json(res, 200, { grant: "pg_fake", expires_at: new Date(Date.now() + 300_000).toISOString() });
  return true;
}
