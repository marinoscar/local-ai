import { describe, expect, it } from "vitest";
import {
  checkName,
  doctorView,
  drillView,
  eventSentence,
  eventSentences,
  levelOfPriority,
  type DoctorReport,
  type DrillDoc,
} from "./health";

const report = (results: DoctorReport["results"], passed = ["a.b", "c.d"]): DoctorReport => ({
  ran_at: "2026-11-14T12:00:00Z",
  duration_ms: 10,
  summary: { ok: passed.length, info: 0, warning: 0, error: 0, critical: 0 },
  results,
  passed,
});

describe("doctorView (agent.md section 3.8.27)", () => {
  it("is all OK with no problem, and counts the passed checks", () => {
    const v = doctorView(report([]));
    expect(v).toEqual({ passed: 2, problems: [], notes: [], allOk: true });
  });
  it("keeps warnings and errors as problems and info results as notes", () => {
    const w = { check: "svc.kiwix", code: "LAI-SVC-002", severity: "warning" as const, title: "t", fix: "f", action: "service.restart:kiwix" };
    const i = { check: "agt.upgrade", code: "LAI-AGT-015", severity: "info" as const, title: "t", fix: "f" };
    const v = doctorView(report([w, i]));
    expect(v.problems).toEqual([w]);
    expect(v.notes).toEqual([i]);
    expect(v.allOk).toBe(false);
  });
  it("only notes is still all OK", () => {
    const i = { check: "agt.upgrade", code: "LAI-AGT-015", severity: "info" as const, title: "t", fix: "f" };
    expect(doctorView(report([i])).allOk).toBe(true);
  });
});

describe("eventSentence (agent.md section 3.8.23)", () => {
  it("maps syslog priorities to four levels", () => {
    expect([0, 3, 4, 5, 6, 7].map(levelOfPriority)).toEqual(["error", "error", "warning", "info", "info", "debug"]);
  });
  it("shows the msg, level and code of an agent JSON line, never the object", () => {
    const e = eventSentence({
      at: "2026-11-14T11:59:10Z",
      priority: 6,
      message: '{"level":"WARN","msg":"the library is not answering","code":"LAI-SVC-002","component":"services"}',
    });
    expect(e.text).toBe("the library is not answering");
    expect(e.level).toBe("warning");
    expect(e.code).toBe("LAI-SVC-002");
    expect(e.at?.toISOString()).toBe("2026-11-14T11:59:10.000Z");
    expect(e.text).not.toContain("{");
  });
  it("keeps a plain line and finds a code in it", () => {
    const e = eventSentence({ at: "2026-11-14T11:59:30Z", priority: 3, message: "  load failed LAI-LLM-008 here " });
    expect(e).toMatchObject({ level: "error", text: "load failed LAI-LLM-008 here", code: "LAI-LLM-008" });
  });
  it("a JSON line without msg names its fields; a broken JSON line stays text; a bad time is null", () => {
    expect(eventSentence({ at: "x", priority: 6, message: '{"level":"ERROR","op":"x"}' })).toMatchObject({
      at: null,
      level: "error",
      text: "op",
    });
    expect(eventSentence({ at: "", priority: 6, message: "{not json" }).text).toBe("{not json");
  });
  it("lists the newest first", () => {
    const list = eventSentences({
      unit: "u",
      truncated: false,
      lines: [
        { at: "2026-11-14T11:00:00Z", priority: 6, message: "first" },
        { at: "2026-11-14T12:00:00Z", priority: 6, message: "second" },
      ],
    });
    expect(list.map((e) => e.text)).toEqual(["second", "first"]);
  });
});

describe("checkName (drill.md section 4.2)", () => {
  it("finds the family by the longest prefix and keeps the rest as a suffix", () => {
    expect(checkName("drl.portal")).toEqual({ family: "portal", suffix: "" });
    expect(checkName("drl.portal.firstaid")).toEqual({ family: "firstaid", suffix: "" });
    expect(checkName("drl.stt.es")).toEqual({ family: "stt", suffix: "es" });
    expect(checkName("drl.llm.answer.qwen3.5-9b-q4km")).toEqual({ family: "answer", suffix: "qwen3.5-9b-q4km" });
    expect(checkName("drl.unknown.thing")).toEqual({ family: "", suffix: "" });
    expect(checkName("drl.portalx")).toEqual({ family: "", suffix: "" });
  });
});

const doc = (extra: Partial<DrillDoc>): DrillDoc => ({
  run_id: "drl-1",
  trigger: "manual",
  quick: true,
  started_at: "2026-11-14T12:00:00Z",
  finished_at: "2026-11-14T12:02:00Z",
  overall: "pass",
  survival_ready: true,
  counts: { pass: 3, warn: 0, fail: 0, skip: 0 },
  checks: [],
  ...extra,
});

describe("drillView (drill.md section 3.4)", () => {
  it("a finished run: outcome, counts, and the checks to look at, failures first", () => {
    const v = drillView(
      doc({
        overall: "fail",
        checks: [
          { id: "drl.fence", area: "net", result: "pass", ms: 1 },
          { id: "drl.stt.es", area: "speech", result: "warn", code: "LAI-DRL-031", ms: 1 },
          { id: "drl.kiwix.search", area: "library", result: "fail", code: "LAI-DRL-030", ms: 1 },
        ],
      }),
    );
    expect(v.outcome).toBe("fail");
    expect(v.attention.map((c) => c.code)).toEqual(["LAI-DRL-030", "LAI-DRL-031"]);
    expect(v.done).toBe(3);
  });
  it("running, aborted and skipped come from state", () => {
    expect(drillView(doc({ state: "running", overall: "running" })).outcome).toBe("running");
    expect(drillView(doc({ state: "aborted", overall: "aborted", reason: "r" })).reason).toBe("r");
    expect(drillView(doc({ state: "skipped", overall: "skipped", code: "LAI-DRL-005" })).code).toBe("LAI-DRL-005");
  });
  it("a manual pull-the-plug record and an unknown overall", () => {
    expect(drillView(doc({ trigger: "manual_plug", overall: "pass" })).outcome).toBe("manual_pass");
    expect(drillView(doc({ trigger: "manual_plug", overall: "fail" })).outcome).toBe("manual_fail");
    expect(drillView(doc({ overall: "strange" })).outcome).toBe("fail");
  });
});
