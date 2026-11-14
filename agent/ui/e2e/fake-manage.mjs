// The Manage screens' part of the fake agent (dashboard.md). Each screen keeps its routes in its own
// module under e2e/fake-manage/ (mode, content, models, health, system, pin): it exports
// `handle(req, res, url, ctx)` (true when it answered) and `reset()`. ctx gives the shared pieces of
// fake-agent.mjs: getStatus/setStatus (the Portal status body), record (the /__seen log), readBody,
// json(res, code, body), emit(type, data) (one event on every open stream).
import * as content from "./fake-manage/content.mjs";
import * as health from "./fake-manage/health.mjs";
import * as mode from "./fake-manage/mode.mjs";
import * as models from "./fake-manage/models.mjs";
import * as pin from "./fake-manage/pin.mjs";
import * as system from "./fake-manage/system.mjs";

const MODULES = [pin, mode, content, models, health, system];

export function resetManage() {
  for (const m of MODULES) m.reset();
}

export async function handleManage(req, res, url, ctx) {
  for (const m of MODULES) if (await m.handle(req, res, url, ctx)) return true;
  return false;
}
