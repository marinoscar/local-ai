// GET /api/v1/mode and POST /api/v1/mode (agent.md section 3.8.15). A change commits at once (the
// fake has no steps) unless POST /__mode?slow=1 keeps it running until POST /__mode?finish=1.
// POST /__mode?busy=1 makes the next POST /mode answer 409 LAI-AGT-022.
let slow = false;
let busy = false;
let pending = null;
let connectivity = "full";

export function reset() {
  slow = false;
  busy = false;
  pending = null;
  connectivity = "full";
}

export async function handle(req, res, url, ctx) {
  const p = url.pathname;
  if (p === "/__mode") {
    if (url.searchParams.has("slow")) slow = url.searchParams.get("slow") === "1";
    if (url.searchParams.has("busy")) busy = url.searchParams.get("busy") === "1";
    if (url.searchParams.has("connectivity")) connectivity = url.searchParams.get("connectivity");
    if (url.searchParams.get("finish") === "1" && pending) {
      ctx.setStatus({ mode: pending.target });
      pending = null;
    }
    ctx.json(res, 200, {});
    return true;
  }
  if (p !== "/api/v1/mode") return false;
  const st = ctx.getStatus();
  if (req.method === "GET") {
    ctx.json(res, 200, {
      mode: st.mode,
      since: null,
      transition: pending ? { op_id: "op_fake", from: st.mode, target: pending.target, step: "link_stop", warnings: [] } : null,
      allowed_targets: ["online", "offline", "survival"].filter((m) => m !== st.mode),
      leave_requires_admin: false,
      connectivity,
      suggest_survival: false,
    });
    return true;
  }
  if (req.method !== "POST") return false;
  const body = JSON.parse((await ctx.readBody(req)) || "{}");
  ctx.record({ path: p, method: "POST", body, grant: req.headers["x-localai-pin-grant"] ?? null });
  if (busy) {
    busy = false;
    ctx.json(res, 409, { error: { code: "LAI-AGT-022", message: "Another mode change is running", fix: "Wait for it to finish, then try again.", details: { op_id: "op_other" } } });
    return true;
  }
  if (st.session !== "full" && st.mode === "survival") {
    ctx.json(res, 403, { error: { code: "LAI-SEC-002", message: "Leaving survival mode needs the Local AI app", details: { pin_required: true } } });
    return true;
  }
  if (slow) {
    pending = { target: body.target };
    ctx.json(res, 202, { op_id: "op_fake", from: st.mode, target: body.target });
    return true;
  }
  ctx.setStatus({ mode: body.target });
  ctx.emit("mode.changed", { to: body.target });
  ctx.json(res, 202, { op_id: "op_fake", from: st.mode, target: body.target });
  return true;
}
