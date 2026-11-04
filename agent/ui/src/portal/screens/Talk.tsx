import { useEffect, useReducer, useRef, useState } from "react";
import { ApiError, apiFetch } from "../api";
import { ConvertError, recordingToWav, type Recording } from "../audio/wav";
import { buildChatBody, streamChat, type ChatMessage } from "../chat/stream";
import { announce } from "../components/LiveRegion";
import { isLang, t, useLang, type Key, type Lang } from "../i18n/i18n";
import { talkPrompt } from "../prompts";
import { navigate } from "../router";
import {
  makeAudioPlayer,
  makeFetchSpeech,
  listVoices,
  pickVoice,
  storeVoice,
  storedVoice,
  type Voice,
} from "../speech/player";
import { SentenceSplitter } from "../speech/sentences";
import { replay, TtsQueue } from "../speech/ttsQueue";
import type { PortalStatus } from "../status";
import {
  errorReason,
  INITIAL,
  reduce,
  statusKey,
  type TalkError,
  type TalkStep,
} from "../talk/machine";
import "../styles/talk.css";
import { MicIcon } from "../talk/MicIcon";
import { Recorder, RecorderError } from "../talk/recorder";
import { formatClock, meterFraction, MAX_RECORD_MS } from "../talk/vad";

// Talk (survival-portal.md sections 4.1.4 and 4.5): record, convert to WAV in the browser, transcribe,
// stream the answer from the loaded brain and speak it sentence by sentence.

type TalkLang = "auto" | "en" | "es";
const LANG_KEY = "lai.portal.talk.lang";
const AUTO_KEY = "lai.portal.talk.autoSend";
const AUTO_SEND_MS = 1500;
const HISTORY_MESSAGES = 6;

const LANG_OPTIONS: { value: TalkLang; label: Key | null; own: string }[] = [
  { value: "auto", label: "talk.auto", own: "" },
  { value: "en", label: null, own: "English" },
  { value: "es", label: null, own: "Español" },
];

function readPref(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writePref(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Storage blocked: the choice lasts until reload.
  }
}

function aborted(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}

function codeOf(e: unknown): { code: string; status: number } {
  if (e instanceof ApiError) return { code: e.code, status: e.status };
  if (e instanceof RecorderError || e instanceof ConvertError)
    return { code: e.code, status: 0 };
  return { code: "", status: 0 };
}

const REASON_TEXT: Record<string, Key> = {
  silent: "talk.silent",
  busy: "talk.busy",
  slow: "talk.slow",
  unavailable: "talk.unavailable",
  nomic: "talk.noMic",
  norecord: "talk.noRecord",
  badaudio: "talk.badAudio",
  nobrain: "chat.noBrain",
  failed: "talk.failed",
};

function inTextField(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return (
    tag === "INPUT" ||
    tag === "TEXTAREA" ||
    tag === "SELECT" ||
    tag === "BUTTON" ||
    tag === "A" ||
    target.isContentEditable
  );
}

export function Talk({ status }: { status: PortalStatus | null }) {
  const uiLang = useLang();
  const [ctx, dispatch] = useReducer(reduce, INITIAL);
  const [talkLang, setTalkLang] = useState<TalkLang>(() => {
    const v = readPref(LANG_KEY);
    return v === "en" || v === "es" ? v : "auto";
  });
  const [autoSend, setAutoSend] = useState(() => readPref(AUTO_KEY) !== "0");
  const [voices, setVoices] = useState<Voice[]>([]);
  const [voiceTick, setVoiceTick] = useState(0);
  const [transcript, setTranscript] = useState("");
  const [answer, setAnswer] = useState("");
  const [answerLang, setAnswerLang] = useState<Lang>(uiLang);
  const [textOnly, setTextOnly] = useState(false);
  const [replaying, setReplaying] = useState(false);
  const [canRepeat, setCanRepeat] = useState(false);
  const [db, setDb] = useState(-Infinity);
  const [elapsed, setElapsed] = useState(0);
  const [countdown, setCountdown] = useState(0);

  const transcriptRef = useRef("");
  const sttLangRef = useRef<Lang | null>(null);
  const recorderRef = useRef<Recorder | null>(null);
  const recRef = useRef<Recording | null>(null);
  const wavRef = useRef<Blob | null>(null);
  const ctlRef = useRef<AbortController | null>(null);
  const replayCtlRef = useRef<AbortController | null>(null);
  const queueRef = useRef<TtsQueue | null>(null);
  const historyRef = useRef<ChatMessage[]>([]);
  const playerRef = useRef<ReturnType<typeof makeAudioPlayer> | null>(null);
  const talkLangRef = useRef(talkLang);
  talkLangRef.current = talkLang;
  const statusRef = useRef(status);
  statusRef.current = status;
  const voicesRef = useRef(voices);
  voicesRef.current = voices;
  const uiLangRef = useRef(uiLang);
  uiLangRef.current = uiLang;

  const player = () => {
    if (!playerRef.current) playerRef.current = makeAudioPlayer();
    return playerRef.current;
  };

  useEffect(() => {
    let live = true;
    void listVoices().then((v) => {
      if (live) setVoices(v);
    });
    return () => {
      live = false;
    };
  }, []);

  /** The language of the answer: a fixed choice, else what the recognizer heard, else the UI language. */
  const effLang = (): Lang =>
    talkLangRef.current !== "auto"
      ? talkLangRef.current
      : (sttLangRef.current ?? uiLangRef.current);

  const setTranscriptBoth = (v: string) => {
    transcriptRef.current = v;
    setTranscript(v);
  };

  /** abortWork cancels every request, the speech and a recording in progress. */
  const abortWork = () => {
    ctlRef.current?.abort();
    ctlRef.current = null;
    queueRef.current?.stop();
    replayCtlRef.current?.abort();
    replayCtlRef.current = null;
    recorderRef.current?.cancel();
    recorderRef.current = null;
    setReplaying(false);
  };

  const newCtl = (): AbortController => {
    ctlRef.current?.abort();
    const c = new AbortController();
    ctlRef.current = c;
    return c;
  };

  const fail = (step: TalkStep, e: unknown, forced?: string) => {
    const { code, status: http } = codeOf(e);
    const error: TalkError = {
      step,
      code: forced ?? code,
      reason: errorReason(forced ?? code, http),
    };
    dispatch({ type: "FAIL", error });
    announce(t(REASON_TEXT[error.reason] ?? "talk.failed"), true);
  };

  const startRecording = async () => {
    abortWork();
    setTranscriptBoth("");
    setAnswer("");
    setTextOnly(false);
    setCanRepeat(false);
    setElapsed(0);
    setDb(-Infinity);
    sttLangRef.current = null;
    const rec = new Recorder({
      onTick: (level, ms) => {
        setDb(level);
        setElapsed(ms);
      },
      onAutoStop: () => void stopRecording(),
    });
    recorderRef.current = rec;
    try {
      await rec.start();
      if (recorderRef.current !== rec) {
        rec.cancel(); // Esc came while the browser asked for the microphone
        return;
      }
      dispatch({ type: "REC_STARTED" });
    } catch (e) {
      if (recorderRef.current !== rec) return; // cancelled meanwhile
      recorderRef.current = null;
      fail("record", e);
    }
  };

  const stopRecording = async () => {
    const rec = recorderRef.current;
    if (!rec) return;
    recorderRef.current = null;
    const ctl = newCtl();
    dispatch({ type: "REC_STOPPED" });
    try {
      recRef.current = await rec.stop();
    } catch (e) {
      if (!ctl.signal.aborted) fail("convert", e, "LAI-SVC-105");
      return;
    }
    if (!ctl.signal.aborted) await runConvert(ctl);
  };

  const runConvert = async (ctl: AbortController) => {
    try {
      const rec = recRef.current;
      if (!rec) throw new ConvertError("no recording");
      const wav = await recordingToWav(rec);
      if (ctl.signal.aborted) return;
      wavRef.current = wav;
      dispatch({ type: "CONVERTED" });
      await runTranscribe(ctl);
    } catch (e) {
      if (!ctl.signal.aborted) fail("convert", e, "LAI-SVC-105");
    }
  };

  const runTranscribe = async (ctl: AbortController) => {
    try {
      const wav = wavRef.current;
      if (!wav) throw new ConvertError("no audio");
      const form = new FormData();
      form.append("audio", wav, "speech.wav");
      form.append("language", talkLangRef.current);
      const res = await apiFetch("/api/v1/speech/stt", {
        method: "POST",
        body: form,
        signal: ctl.signal,
      });
      const body = (await res.json()) as { text?: string; language?: string };
      if (ctl.signal.aborted) return;
      const text = (body.text ?? "").trim();
      if (!text) {
        fail("transcribe", null, "LAI-SVC-014");
        return;
      }
      sttLangRef.current = isLang(body.language) ? body.language : null;
      setTranscriptBoth(text);
      dispatch({ type: "TRANSCRIBED" });
    } catch (e) {
      if (!aborted(e) && !ctl.signal.aborted) fail("transcribe", e);
    }
  };

  const runThink = async () => {
    const ctl = newCtl();
    const brain = statusRef.current?.brain.loaded[0];
    if (!brain) {
      fail("think", null, "LAI-SVC-106");
      return;
    }
    const lang = effLang();
    setAnswerLang(lang);
    setAnswer("");
    setTextOnly(false);
    setCanRepeat(false);
    const splitter = new SentenceSplitter();
    const queue = new TtsQueue(
      {
        fetchWav: makeFetchSpeech(() => ({
          language: lang,
          voice: pickVoice(voicesRef.current, lang),
        })),
        play: player(),
      },
      { onTextOnly: () => setTextOnly(true) },
    );
    queueRef.current = queue;
    const user = transcriptRef.current;
    let gotToken = false;
    let speaking = false;
    const speak = (sentences: string[]) => {
      for (const s of sentences) {
        if (!speaking) {
          speaking = true;
          dispatch({ type: "SPEECH_START" });
        }
        queue.enqueue(s);
      }
    };
    try {
      const full = await streamChat({
        body: buildChatBody({
          model: brain.id,
          system: talkPrompt(lang),
          history: historyRef.current.slice(-HISTORY_MESSAGES),
          user,
          reasoning: brain.roles?.includes("reasoning") === true,
        }),
        signal: ctl.signal,
        onText: (piece) => {
          if (!gotToken) {
            gotToken = true;
            dispatch({ type: "FIRST_TOKEN" });
          }
          setAnswer((a) => a + piece);
          speak(splitter.push(piece));
        },
      });
      if (ctl.signal.aborted) return;
      speak(splitter.flush());
      queue.end();
      historyRef.current = [
        ...historyRef.current,
        { role: "user" as const, content: user },
        { role: "assistant" as const, content: full },
      ].slice(-HISTORY_MESSAGES);
      await queue.finished;
      if (ctl.signal.aborted) return;
      setCanRepeat(queue.cached.length > 0);
      dispatch({ type: "DONE" });
    } catch (e) {
      queue.stop();
      if (!aborted(e) && !ctl.signal.aborted) fail("think", e);
    }
  };

  // Entering "thinking" (Send, the auto-send, or Try again) starts the chat request.
  useEffect(() => {
    if (ctx.state === "thinking") void runThink();
    // runThink reads everything it needs from refs, so only the state matters here.
  }, [ctx.state]);

  // The auto-send: 1.5 s after the transcript appears unless the person edits or redoes.
  useEffect(() => {
    if (ctx.state !== "confirm" || ctx.editing || !autoSend) {
      setCountdown(0);
      return;
    }
    const t0 = performance.now();
    setCountdown(AUTO_SEND_MS);
    const tick = window.setInterval(() => {
      setCountdown(Math.max(0, AUTO_SEND_MS - (performance.now() - t0)));
    }, 100);
    const go = window.setTimeout(
      () => dispatch({ type: "SEND" }),
      AUTO_SEND_MS,
    );
    return () => {
      window.clearInterval(tick);
      window.clearTimeout(go);
    };
  }, [ctx.state, ctx.editing, autoSend]);

  const onToggle = () => {
    if (ctx.state === "idle") void startRecording();
    else if (ctx.state === "recording") void stopRecording();
  };

  const onStopAll = () => {
    setCanRepeat((queueRef.current?.cached.length ?? 0) > 0);
    abortWork();
    dispatch({ type: "ESC" });
  };

  const onRepeat = () => {
    const wavs = queueRef.current?.cached ?? [];
    if (wavs.length === 0) return;
    replayCtlRef.current?.abort();
    const c = new AbortController();
    replayCtlRef.current = c;
    setReplaying(true);
    void replay(wavs, player(), c.signal).finally(() => {
      if (replayCtlRef.current === c) {
        replayCtlRef.current = null;
        setReplaying(false);
      }
    });
  };

  const onTryAgain = () => {
    const err = ctx.error;
    if (!err) return;
    if (err.step === "record") {
      void startRecording();
      return;
    }
    dispatch({ type: "RETRY" });
    if (err.step === "convert") {
      void runConvert(newCtl());
    } else if (err.step === "transcribe") {
      void runTranscribe(newCtl());
    }
    // "think" starts from the effect on the thinking state.
  };

  // Keys: Space starts and stops the recording, Esc stops whatever is going on (and only then goes
  // Home, on the next press). The capture phase runs before the Portal's global Esc handler.
  const keyState = useRef({ ctx, replaying });
  keyState.current = { ctx, replaying };
  const toggleRef = useRef(onToggle);
  toggleRef.current = onToggle;
  const stopAllRef = useRef(onStopAll);
  stopAllRef.current = onStopAll;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const { ctx: c, replaying: rp } = keyState.current;
      if (e.key === "Escape") {
        if (c.state !== "idle" || c.error || rp) {
          e.preventDefault();
          e.stopImmediatePropagation();
          stopAllRef.current();
        }
      } else if (
        e.key === " " &&
        !e.repeat &&
        !e.ctrlKey &&
        !e.altKey &&
        !e.metaKey &&
        !inTextField(e.target)
      ) {
        e.preventDefault();
        toggleRef.current();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);

  // Leaving the screen stops everything.
  useEffect(() => {
    return () => {
      ctlRef.current?.abort();
      queueRef.current?.stop();
      replayCtlRef.current?.abort();
      recorderRef.current?.cancel();
    };
  }, []);

  const sk = statusKey(ctx.state);
  useEffect(() => {
    if (sk) announce(t(sk));
  }, [sk, uiLang]);

  const err = ctx.error;
  const busy = ctx.state !== "idle";
  const speakingNow =
    ctx.state === "thinking" ||
    ctx.state === "streaming" ||
    ctx.state === "speaking" ||
    replaying;
  const voiceLang: Lang = talkLang === "auto" ? uiLang : talkLang;
  const voiceChoices = voices.filter((v) => v.language === voiceLang);
  const voiceValue = (() => {
    void voiceTick;
    const id = storedVoice(voiceLang);
    return id && voiceChoices.some((v) => v.id === id) ? id : "";
  })();

  const changeLang = (v: TalkLang) => {
    setTalkLang(v);
    writePref(LANG_KEY, v);
  };

  return (
    <section className="screen talk" aria-labelledby="screen-title">
      <div className="talk-bar">
        <h1 id="screen-title" className="screen-title">
          {t("talk.title")}
        </h1>
        <div className="seg" role="group" aria-label={t("talk.language")}>
          {LANG_OPTIONS.map((o) => (
            <button
              key={o.value}
              type="button"
              className="seg-btn"
              lang={o.value === "auto" ? undefined : o.value}
              aria-pressed={talkLang === o.value}
              disabled={busy}
              onClick={() => changeLang(o.value)}
            >
              {o.label ? t(o.label) : o.own}
            </button>
          ))}
        </div>
        <label className="talk-voice">
          <span>{t("talk.voice")}</span>
          <select
            value={voiceValue}
            disabled={busy}
            onChange={(e) => {
              storeVoice(voiceLang, e.target.value);
              setVoiceTick((n) => n + 1);
            }}
          >
            <option value="">{t("talk.voice.auto")}</option>
            {voiceChoices.map((v) => (
              <option key={v.id} value={v.id}>
                {v.display_name}
              </option>
            ))}
          </select>
        </label>
        <label className="talk-auto">
          <input
            type="checkbox"
            checked={autoSend}
            onChange={(e) => {
              setAutoSend(e.target.checked);
              writePref(AUTO_KEY, e.target.checked ? "1" : "0");
            }}
          />
          <span>{t("talk.autoSend")}</span>
        </label>
      </div>

      {err ? (
        <div className="talk-error" role="alert">
          <p className="lead">{t(`talk.step.${err.step}` as Key)}</p>
          <p className="lead">{t(REASON_TEXT[err.reason] ?? "talk.failed")}</p>
          {err.reason === "nomic" ? (
            <p className="lead">{t("talk.micHelp")}</p>
          ) : null}
          {err.code ? (
            <p className="code">{t("common.code", { code: err.code })}</p>
          ) : null}
          <div className="actions">
            {err.reason !== "norecord" ? (
              <button
                type="button"
                className="btn btn-primary"
                onClick={onTryAgain}
              >
                {t("common.tryAgain")}
              </button>
            ) : null}
            {err.reason === "nobrain" ? (
              <button
                type="button"
                className="btn"
                onClick={() => navigate("brain")}
              >
                {t("brain.title")}
              </button>
            ) : null}
          </div>
        </div>
      ) : (
        <div className="talk-mic">
          <button
            type="button"
            className={
              ctx.state === "recording" ? "mic mic-recording" : "mic"
            }
            aria-keyshortcuts="Space"
            disabled={ctx.state !== "idle" && ctx.state !== "recording"}
            onClick={onToggle}
          >
            <MicIcon />
            <span className="mic-text">
              {ctx.state === "recording"
                ? t("talk.stopRecording")
                : t("talk.press")}
            </span>
          </button>
          {ctx.state === "recording" ? (
            <div className="talk-meter">
              <meter
                min={0}
                max={1}
                value={meterFraction(db)}
                aria-label={t("talk.level")}
              />
              <span className="talk-timer">
                {`${formatClock(elapsed)} / ${formatClock(MAX_RECORD_MS)}`}
              </span>
            </div>
          ) : null}
        </div>
      )}

      {transcript ? (
        <div className="talk-line">
          <p className="talk-label">{t("talk.youSaid")}</p>
          {ctx.state === "confirm" && ctx.editing ? (
            <textarea
              className="talk-edit"
              rows={3}
              value={transcript}
              aria-label={t("talk.youSaid")}
              onChange={(e) => setTranscriptBoth(e.target.value)}
            />
          ) : (
            <p className="talk-text" lang={answerLang}>
              {transcript}
            </p>
          )}
          {ctx.state === "confirm" ? (
            <div className="actions">
              {!ctx.editing ? (
                <button
                  type="button"
                  className="btn"
                  onClick={() => dispatch({ type: "EDIT" })}
                >
                  {t("talk.edit")}
                </button>
              ) : null}
              <button
                type="button"
                className="btn"
                onClick={() => void startRecording()}
              >
                {t("talk.redo")}
              </button>
              <button
                type="button"
                className="btn btn-primary"
                disabled={transcript.trim() === ""}
                onClick={() => dispatch({ type: "SEND" })}
              >
                {t("talk.send")}
              </button>
              {countdown > 0 ? (
                <span className="talk-count">
                  {t("talk.sendingIn", { s: Math.ceil(countdown / 1000) })}
                </span>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}

      {answer ? (
        <div className="talk-line">
          <p className="talk-label">{t("talk.answer")}</p>
          <p className="talk-answer" lang={answerLang}>
            {answer}
          </p>
          {textOnly ? <p className="banner-note">{t("talk.textOnly")}</p> : null}
          <div className="actions">
            {speakingNow ? (
              <button type="button" className="btn" onClick={onStopAll}>
                {t("common.stopSpeaking")}
              </button>
            ) : null}
            {!speakingNow && canRepeat && !textOnly ? (
              <button type="button" className="btn" onClick={onRepeat}>
                {t("common.repeat")}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}

      <p className="talk-status" role="status">
        {sk ? t(sk) : ""}
      </p>
    </section>
  );
}
