// The Talk state machine (survival-portal.md section 4.5), pure and table-testable. The screen does
// the work (record, convert, call the agent) and tells the machine what happened; an event that
// does not fit the current state is ignored, so a late answer of an aborted step can't move it.

export type TalkState =
  | "idle"
  | "recording"
  | "converting"
  | "transcribing"
  | "confirm"
  | "thinking"
  | "streaming"
  | "speaking";

/** The step that failed, for the error screen: the step, the code, and Try again. */
export type TalkStep = "record" | "convert" | "transcribe" | "think";

export interface TalkError {
  step: TalkStep;
  /** A doctor code (LAI-SVC-nnn) or "" when the agent sent none. */
  code: string;
  /** A stable word the screen maps to text: silent, busy, slow, unavailable, nomic, norecord, badaudio, nobrain, failed. */
  reason: string;
}

export interface TalkContext {
  state: TalkState;
  error: TalkError | null;
  /** The transcript is being edited by hand (cancels the auto-send). */
  editing: boolean;
}

export type TalkEvent =
  | { type: "REC_STARTED" }
  | { type: "REC_STOPPED" }
  | { type: "CONVERTED" }
  | { type: "TRANSCRIBED" }
  | { type: "EDIT" }
  | { type: "SEND" }
  | { type: "FIRST_TOKEN" }
  | { type: "SPEECH_START" }
  | { type: "DONE" }
  | { type: "FAIL"; error: TalkError }
  | { type: "RETRY" }
  | { type: "ESC" };

export const INITIAL: TalkContext = {
  state: "idle",
  error: null,
  editing: false,
};

const ACTIVE: ReadonlySet<TalkState> = new Set([
  "recording",
  "converting",
  "transcribing",
  "confirm",
  "thinking",
  "streaming",
  "speaking",
]);

/** The state a Try again returns to, per failed step. */
const RETRY_STATE: Record<TalkStep, TalkState> = {
  record: "recording",
  convert: "converting",
  transcribe: "transcribing",
  think: "thinking",
};

export function reduce(ctx: TalkContext, ev: TalkEvent): TalkContext {
  const at = (state: TalkState, editing = false): TalkContext => ({
    state,
    error: null,
    editing,
  });
  const s = ctx.state;
  switch (ev.type) {
    case "ESC":
      // Esc stops whatever is going on; in idle it changes nothing (the screen then goes Home).
      return ACTIVE.has(s) || ctx.error ? at("idle") : ctx;
    case "FAIL":
      return ACTIVE.has(s) || s === "idle"
        ? { state: "idle", error: ev.error, editing: false }
        : ctx;
    case "REC_STARTED":
      return s === "idle" || s === "confirm" ? at("recording") : ctx;
    case "REC_STOPPED":
      return s === "recording" ? at("converting") : ctx;
    case "CONVERTED":
      return s === "converting" ? at("transcribing") : ctx;
    case "TRANSCRIBED":
      return s === "transcribing" ? at("confirm") : ctx;
    case "EDIT":
      return s === "confirm" ? { ...ctx, editing: true } : ctx;
    case "SEND":
      return s === "confirm" ? at("thinking") : ctx;
    case "FIRST_TOKEN":
      return s === "thinking" ? at("streaming") : ctx;
    case "SPEECH_START":
      return s === "thinking" || s === "streaming" ? at("speaking") : ctx;
    case "DONE":
      return s === "thinking" || s === "streaming" || s === "speaking"
        ? at("idle")
        : ctx;
    case "RETRY":
      return s === "idle" && ctx.error ? at(RETRY_STATE[ctx.error.step]) : ctx;
  }
}

/** The status-line key of a state (survival-portal.md section 4.1.4); idle has none. */
export function statusKey(
  state: TalkState,
): "talk.listening" | "talk.writing" | "talk.thinking" | "talk.speaking" | null {
  switch (state) {
    case "recording":
      return "talk.listening";
    case "converting":
    case "transcribing":
      return "talk.writing";
    case "thinking":
    case "streaming":
      return "talk.thinking";
    case "speaking":
      return "talk.speaking";
    default:
      return null;
  }
}

/** errorReason maps an agent code to the reason word of a failed step (survival-portal.md section 8). */
export function errorReason(code: string, status: number): string {
  switch (code) {
    case "LAI-SVC-014":
      return "silent";
    case "LAI-SVC-015":
      return "busy";
    case "LAI-SVC-016":
      return "slow";
    case "LAI-SVC-010":
    case "LAI-SVC-011":
      return "unavailable";
    case "LAI-SVC-106":
      return "nobrain";
    case "LAI-SVC-103":
      return "nomic";
    case "LAI-SVC-104":
      return "norecord";
    case "LAI-SVC-105":
      return "badaudio";
  }
  if (status === 413) return "silent";
  if (status === 429) return "busy";
  if (status === 504) return "slow";
  return "failed";
}
