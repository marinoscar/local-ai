import { describe, expect, it } from "vitest";
import {
  errorReason,
  INITIAL,
  reduce,
  statusKey,
  type TalkContext,
  type TalkEvent,
  type TalkStep,
  type TalkState,
} from "./machine";

const ctx = (state: TalkState, editing = false): TalkContext => ({
  state,
  error: null,
  editing,
});
const run = (events: TalkEvent[], from: TalkContext = INITIAL) =>
  events.reduce(reduce, from);
const ERR = { step: "transcribe" as TalkStep, code: "LAI-SVC-014", reason: "silent" };

describe("happy path", () => {
  it("walks idle to idle through every state", () => {
    const seen: TalkState[] = [];
    let c = INITIAL;
    for (const e of [
      "REC_STARTED",
      "REC_STOPPED",
      "CONVERTED",
      "TRANSCRIBED",
      "SEND",
      "FIRST_TOKEN",
      "SPEECH_START",
      "DONE",
    ] as const) {
      c = reduce(c, { type: e });
      seen.push(c.state);
    }
    expect(seen).toEqual([
      "recording",
      "converting",
      "transcribing",
      "confirm",
      "thinking",
      "streaming",
      "speaking",
      "idle",
    ]);
  });

  it("a text-only answer goes from streaming to idle", () => {
    expect(
      run([{ type: "SEND" }], ctx("confirm")).state,
    ).toBe("thinking");
    expect(run([{ type: "DONE" }], ctx("streaming")).state).toBe("idle");
  });

  it("a short answer can start speaking before the first token event", () => {
    expect(run([{ type: "SPEECH_START" }], ctx("thinking")).state).toBe(
      "speaking",
    );
  });
});

describe("Esc in every state", () => {
  const states: TalkState[] = [
    "recording",
    "converting",
    "transcribing",
    "confirm",
    "thinking",
    "streaming",
    "speaking",
  ];
  for (const s of states) {
    it(`${s} goes to idle`, () => {
      expect(reduce(ctx(s), { type: "ESC" })).toEqual(ctx("idle"));
    });
  }
  it("idle stays idle (the screen then goes Home)", () => {
    expect(reduce(INITIAL, { type: "ESC" })).toBe(INITIAL);
  });
  it("clears an error shown in idle", () => {
    const withErr: TalkContext = { ...INITIAL, error: ERR };
    expect(reduce(withErr, { type: "ESC" })).toEqual(INITIAL);
  });
});

describe("confirm", () => {
  it("Redo starts a new recording", () => {
    expect(run([{ type: "REC_STARTED" }], ctx("confirm")).state).toBe(
      "recording",
    );
  });
  it("Edit stops the auto-send until Send", () => {
    const e = run([{ type: "EDIT" }], ctx("confirm"));
    expect(e).toEqual(ctx("confirm", true));
    expect(run([{ type: "SEND" }], e)).toEqual(ctx("thinking"));
  });
  it("Edit is ignored outside confirm", () => {
    expect(reduce(ctx("thinking"), { type: "EDIT" })).toEqual(ctx("thinking"));
  });
});

describe("failures", () => {
  const steps: [TalkState, TalkEvent["type"]][] = [
    ["recording", "FAIL"],
    ["converting", "FAIL"],
    ["transcribing", "FAIL"],
    ["thinking", "FAIL"],
    ["streaming", "FAIL"],
    ["speaking", "FAIL"],
  ];
  for (const [s] of steps) {
    it(`a failure in ${s} shows the error in idle`, () => {
      const c = reduce(ctx(s), { type: "FAIL", error: ERR });
      expect(c.state).toBe("idle");
      expect(c.error).toEqual(ERR);
    });
  }

  it("Try again returns to the failed step, keeping the recording", () => {
    const cases: [TalkStep, TalkState][] = [
      ["record", "recording"],
      ["convert", "converting"],
      ["transcribe", "transcribing"],
      ["think", "thinking"],
    ];
    for (const [step, to] of cases) {
      const c = run([{ type: "RETRY" }], {
        state: "idle",
        error: { ...ERR, step },
        editing: false,
      });
      expect(c).toEqual(ctx(to));
    }
  });

  it("Try again without an error does nothing", () => {
    expect(run([{ type: "RETRY" }])).toBe(INITIAL);
  });
});

describe("events that do not fit are ignored", () => {
  it("late answers of an aborted step", () => {
    for (const e of [
      "REC_STOPPED",
      "CONVERTED",
      "TRANSCRIBED",
      "SEND",
      "FIRST_TOKEN",
      "SPEECH_START",
      "DONE",
    ] as const) {
      expect(reduce(INITIAL, { type: e })).toBe(INITIAL);
    }
  });
  it("a recording can't start while the answer is spoken", () => {
    expect(reduce(ctx("speaking"), { type: "REC_STARTED" }).state).toBe(
      "speaking",
    );
  });
});

describe("status strings and error reasons", () => {
  it("maps states to the four status keys", () => {
    expect(statusKey("idle")).toBeNull();
    expect(statusKey("confirm")).toBeNull();
    expect(statusKey("recording")).toBe("talk.listening");
    expect(statusKey("converting")).toBe("talk.writing");
    expect(statusKey("transcribing")).toBe("talk.writing");
    expect(statusKey("thinking")).toBe("talk.thinking");
    expect(statusKey("streaming")).toBe("talk.thinking");
    expect(statusKey("speaking")).toBe("talk.speaking");
  });
  it("maps agent codes to reasons", () => {
    expect(errorReason("LAI-SVC-014", 400)).toBe("silent");
    expect(errorReason("", 413)).toBe("silent");
    expect(errorReason("LAI-SVC-015", 429)).toBe("busy");
    expect(errorReason("LAI-SVC-010", 503)).toBe("unavailable");
    expect(errorReason("LAI-SVC-011", 503)).toBe("unavailable");
    expect(errorReason("LAI-SVC-016", 504)).toBe("slow");
    expect(errorReason("", 500)).toBe("failed");
  });
});
