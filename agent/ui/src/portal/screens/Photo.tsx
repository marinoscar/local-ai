import { useEffect, useRef, useState, type DragEvent } from "react";
import { ApiError, apiFetch } from "../api";
import { streamChat } from "../chat/stream";
import { announce } from "../components/LiveRegion";
import { t, useLang, type Key } from "../i18n/i18n";
import { canAsk, photoBody, type Turn } from "../photo/convo";
import { PhotoError, preparePhoto, toDataUrl } from "../photo/prepare";
import { photoPrompt } from "../prompts";
import { navigate } from "../router";
import { makeAudioPlayer, makeFetchSpeech } from "../speech/player";
import { splitSentences } from "../speech/sentences";
import { TtsQueue } from "../speech/ttsQueue";
import { refreshStatus, type PortalStatus } from "../status";
import "../styles/photo.css";
import "../styles/talk.css";

// Ask about a photo (survival-portal.md sections 4.1.3 and 4.6): get a photo, prepare it in the
// browser (upright, at most 1024 px, JPEG without metadata), offer the brain switch when the
// running brain can't see, stream the answer through /chat/, and read it aloud on request. The
// photo lives in memory only.

const QUICK: Key[] = [
  "photo.q.what",
  "photo.q.danger",
  "photo.q.fix",
  "photo.q.read",
];
const SWITCH_TIMEOUT_MS = 300_000;

interface Prepared {
  previewUrl: string;
  dataUrl: string;
}

function aborted(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}

export function Photo({ status }: { status: PortalStatus | null }) {
  const lang = useLang();
  const [photo, setPhoto] = useState<Prepared | null>(null);
  const [badPhoto, setBadPhoto] = useState(false);
  const [question, setQuestion] = useState("");
  const [turns, setTurns] = useState<Turn[]>([]);
  const [answer, setAnswer] = useState("");
  const [asking, setAsking] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  const [speaking, setSpeaking] = useState(false);
  const [textOnly, setTextOnly] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [switching, setSwitching] = useState<{ id: string; since: number } | null>(
    null,
  );
  const [switchSecs, setSwitchSecs] = useState(0);
  const [switchFailed, setSwitchFailed] = useState(false);

  const takeRef = useRef<HTMLInputElement>(null);
  const chooseRef = useRef<HTMLInputElement>(null);
  const questionRef = useRef<HTMLTextAreaElement>(null);
  const ctlRef = useRef<AbortController | null>(null);
  const queueRef = useRef<TtsQueue | null>(null);
  const playerRef = useRef<ReturnType<typeof makeAudioPlayer> | null>(null);
  const previewRef = useRef<string | null>(null);

  const cap = status?.capabilities.photo;
  const visionBrain = status?.brain.loaded.find((m) => m.vision === true);
  const needsSwitch = !visionBrain && cap?.reason === "needs_switch";
  const switchTarget = cap?.vision_model ?? "";
  const switchName =
    status?.brain.choices?.find((m) => m.id === switchTarget)?.display_name ??
    switchTarget;

  const stopSpeech = () => {
    queueRef.current?.stop();
    queueRef.current = null;
    setSpeaking(false);
  };

  const stopAll = () => {
    ctlRef.current?.abort();
    ctlRef.current = null;
    stopSpeech();
    setAsking(false);
  };

  // Memory only: the preview URL is revoked when the photo changes or the screen is left.
  const replacePreview = (url: string | null) => {
    if (previewRef.current) URL.revokeObjectURL(previewRef.current);
    previewRef.current = url;
  };
  useEffect(
    () => () => {
      replacePreview(null);
      ctlRef.current?.abort();
      queueRef.current?.stop();
    },
    [],
  );

  const resetConversation = () => {
    stopAll();
    setTurns([]);
    setAnswer("");
    setFailed(null);
    setTextOnly(false);
  };

  const takeFile = async (file: File | undefined) => {
    if (!file) return;
    resetConversation();
    setBadPhoto(false);
    try {
      const p = await preparePhoto(file);
      const dataUrl = await toDataUrl(p.blob);
      const previewUrl = URL.createObjectURL(p.blob);
      replacePreview(previewUrl);
      setPhoto({ previewUrl, dataUrl });
      questionRef.current?.focus();
    } catch (e) {
      if (!(e instanceof PhotoError)) console.error(e);
      replacePreview(null);
      setPhoto(null);
      setBadPhoto(true);
      announce(t("photo.bad"), true);
    }
  };

  // The brain switch: ask, then watch the status until the vision model is the one running.
  const startSwitch = async () => {
    setSwitchFailed(false);
    try {
      await apiFetch(`/api/v1/models/${encodeURIComponent(switchTarget)}/load`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ wait: false }),
      });
      setSwitching({ id: switchTarget, since: Date.now() });
    } catch {
      setSwitchFailed(true);
    }
  };
  useEffect(() => {
    if (!switching) return;
    if (status?.brain.loaded.some((m) => m.id === switching.id)) {
      setSwitching(null);
      return;
    }
    const h = window.setInterval(() => {
      const s = Math.round((Date.now() - switching.since) / 1000);
      setSwitchSecs(s);
      if (Date.now() - switching.since > SWITCH_TIMEOUT_MS) {
        setSwitching(null);
        setSwitchFailed(true);
      } else if (s % 3 === 0) {
        void refreshStatus();
      }
    }, 1000);
    return () => window.clearInterval(h);
  }, [switching, status]);

  const ask = async () => {
    const q = question.trim();
    if (!photo || !visionBrain || !q || asking || !canAsk(turns)) return;
    const ctl = new AbortController();
    ctlRef.current = ctl;
    stopSpeech();
    setAsking(true);
    setFailed(null);
    setAnswer("");
    try {
      const full = await streamChat({
        body: photoBody({
          model: visionBrain.id,
          system: photoPrompt(lang),
          turns,
          question: q,
          imageUrl: photo.dataUrl,
        }),
        signal: ctl.signal,
        onText: (piece) => setAnswer((a) => a + piece),
      });
      if (ctl.signal.aborted) return;
      setTurns((ts) => [...ts, { question: q, answer: full }]);
      setQuestion("");
    } catch (e) {
      if (!aborted(e) && !ctl.signal.aborted) {
        setFailed(e instanceof ApiError && e.code ? e.code : "");
        announce(t("photo.failed"), true);
      }
    } finally {
      if (ctlRef.current === ctl) {
        ctlRef.current = null;
        setAsking(false);
      }
    }
  };

  const readAloud = () => {
    stopSpeech();
    if (!playerRef.current) playerRef.current = makeAudioPlayer();
    const queue = new TtsQueue(
      {
        fetchWav: makeFetchSpeech(() => ({ language: lang, voice: null })),
        play: playerRef.current,
      },
      { onTextOnly: () => setTextOnly(true) },
    );
    queueRef.current = queue;
    setTextOnly(false);
    setSpeaking(true);
    for (const s of splitSentences(answer)) queue.enqueue(s);
    queue.end();
    void queue.finished.then(() => {
      if (queueRef.current === queue) {
        queueRef.current = null;
        setSpeaking(false);
      }
    });
  };

  // Esc stops an answer or the speech first; the next Esc goes Home (the Portal's global handler).
  const busyRef = useRef(false);
  busyRef.current = asking || speaking;
  const stopRef = useRef(stopAll);
  stopRef.current = stopAll;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && busyRef.current) {
        e.preventDefault();
        e.stopImmediatePropagation();
        stopRef.current();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    void takeFile(e.dataTransfer.files[0]);
  };

  const limitReached = !canAsk(turns);
  const canSubmit =
    photo !== null &&
    visionBrain !== undefined &&
    question.trim() !== "" &&
    !asking &&
    !limitReached;
  const shownTurnsAnswer = answer !== "";

  return (
    <section className="screen photo" aria-labelledby="screen-title">
      <div className="photo-bar">
        <h1 id="screen-title" className="screen-title">
          {t("photo.title")}
        </h1>
        {visionBrain ? (
          <p className="photo-brain">
            {t("status.brain", { name: visionBrain.display_name })}
          </p>
        ) : null}
      </div>

      {needsSwitch ? (
        <div className="photo-switch" role="group" aria-label={t("photo.switch")}>
          {switching ? (
            <p className="lead" role="status">
              {t("brain.loading", { name: switchName, s: switchSecs })}
            </p>
          ) : (
            <>
              <p className="lead">{t("photo.needsSwitch", { name: switchName })}</p>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => void startSwitch()}
              >
                {t("photo.switch")}
              </button>
            </>
          )}
          {switchFailed ? (
            <p className="banner-note" role="alert">
              {t("brain.failed", { name: switchName })}
            </p>
          ) : null}
        </div>
      ) : null}

      <div className="photo-grid">
        <div className="photo-left">
          <div
            className={dragging ? "photo-drop photo-drop-on" : "photo-drop"}
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={onDrop}
          >
            {photo ? (
              <img
                className="photo-img"
                src={photo.previewUrl}
                alt={t("photo.yourPhoto")}
              />
            ) : (
              <p className="lead">{t("photo.empty")}</p>
            )}
          </div>
          {badPhoto ? (
            <div className="photo-error" role="alert">
              <p className="lead">{t("photo.bad")}</p>
              <p className="code">{t("common.code", { code: "LAI-SVC-108" })}</p>
            </div>
          ) : null}
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => takeRef.current?.click()}
            >
              {t("photo.take")}
            </button>
            <button
              type="button"
              className="btn"
              onClick={() => chooseRef.current?.click()}
            >
              {t("photo.choose")}
            </button>
          </div>
          <input
            ref={takeRef}
            hidden
            type="file"
            accept="image/*"
            capture="environment"
            data-testid="take-input"
            onChange={(e) => {
              void takeFile(e.target.files?.[0]);
              e.target.value = "";
            }}
          />
          <input
            ref={chooseRef}
            hidden
            type="file"
            accept="image/*"
            data-testid="choose-input"
            onChange={(e) => {
              void takeFile(e.target.files?.[0]);
              e.target.value = "";
            }}
          />
        </div>

        <div className="photo-right">
          <label className="talk-label" htmlFor="photo-question">
            {t("photo.question")}
          </label>
          <textarea
            id="photo-question"
            ref={questionRef}
            className="talk-edit"
            rows={3}
            value={question}
            disabled={!photo || limitReached}
            onChange={(e) => setQuestion(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                if (canSubmit) void ask();
              }
            }}
          />
          <p className="talk-label">{t("photo.quick")}</p>
          <div className="photo-chips">
            {QUICK.map((q) => (
              <button
                key={q}
                type="button"
                className="btn"
                disabled={!photo || limitReached}
                onClick={() => {
                  setQuestion(t(q));
                  questionRef.current?.focus();
                }}
              >
                {t(q)}
              </button>
            ))}
          </div>
          {limitReached ? <p className="banner-note">{t("photo.limit")}</p> : null}
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={!canSubmit}
              onClick={() => void ask()}
            >
              {t("photo.ask")}
            </button>
          </div>

          {shownTurnsAnswer || asking || failed !== null ? (
            <div className="photo-answer">
              <p className="talk-label">{t("photo.answer")}</p>
              {asking && answer === "" ? (
                <p className="lead" role="status">
                  {t("common.loading")}
                </p>
              ) : null}
              <p className="talk-answer" lang={lang}>
                {answer}
              </p>
              {failed !== null ? (
                <div className="photo-error" role="alert">
                  <p className="lead">{t("photo.failed")}</p>
                  {failed ? (
                    <p className="code">{t("common.code", { code: failed })}</p>
                  ) : null}
                </div>
              ) : null}
              {textOnly ? (
                <p className="banner-note">{t("talk.textOnly")}</p>
              ) : null}
              <div className="actions">
                {speaking ? (
                  <button type="button" className="btn" onClick={stopSpeech}>
                    {t("common.stopSpeaking")}
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn"
                    disabled={asking || answer === ""}
                    onClick={readAloud}
                  >
                    {t("common.readAloud")}
                  </button>
                )}
                <button
                  type="button"
                  className="btn"
                  disabled={asking}
                  onClick={() => {
                    resetConversation();
                    setQuestion("");
                    questionRef.current?.focus();
                  }}
                >
                  {t("photo.another")}
                </button>
              </div>
            </div>
          ) : null}
        </div>
      </div>
      {failed !== null && failed === "LAI-SVC-106" ? (
        <div className="actions">
          <button type="button" className="btn" onClick={() => navigate("brain")}>
            {t("brain.title")}
          </button>
        </div>
      ) : null}
    </section>
  );
}
