import { useEffect, useRef, useState } from "react";
import "../styles/status-brain.css";
import { announce } from "../components/LiveRegion";
import { brainChoices, isRunning, speedOf } from "../brains";
import { useBrainSwitch } from "../hooks/useBrainSwitch";
import { t, useLang, type Key } from "../i18n/i18n";
import { navigate } from "../router";
import { refreshStatus, type BrainChoice, type PortalStatus } from "../status";

/** One switch in progress: confirm, then progress, then success or failure (survival-portal.md section 4.1.9). */
function Switcher({
  choice,
  status,
  onDone,
}: {
  choice: BrainChoice;
  status: PortalStatus;
  onDone: () => void;
}) {
  const sw = useBrainSwitch(choice.id, status.session);
  const { state } = sw;
  const asked = useRef(false);
  const name = choice.display_name;

  // Open the confirmation once, when the user pressed "Use this".
  useEffect(() => {
    if (asked.current) return;
    asked.current = true;
    sw.ask();
  });

  // A status poll that already shows the model loaded finishes the switch (a missed event).
  const loadedNow = isRunning(choice, status);
  useEffect(() => {
    if (loadedNow) sw.notifyLoaded();
  });

  useEffect(() => {
    if (state.phase === "loaded") {
      announce(t("brain.ready", { name }));
      void refreshStatus();
    } else if (state.phase === "failed") {
      announce(t("brain.failed", { name }), true);
      void refreshStatus();
    }
  }, [state.phase, name]);

  if (state.phase === "confirming") {
    return (
      <div className="panel" role="alertdialog" aria-labelledby="confirm-text">
        <p id="confirm-text" className="lead">
          {t("brain.confirm", { name })}
        </p>
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            autoFocus
            onClick={() => sw.confirm()}
          >
            {t("brain.use")}
          </button>
          <button
            type="button"
            className="btn"
            onClick={() => {
              sw.cancel();
              onDone();
            }}
          >
            {t("common.cancel")}
          </button>
        </div>
      </div>
    );
  }
  if (state.phase === "loading") {
    return (
      <div className="panel" role="status">
        <p className="lead">{t("brain.loading", { name, s: state.seconds })}</p>
        <progress className="meter meter-ok" aria-label={t("brain.title")} />
      </div>
    );
  }
  if (state.phase === "loaded") {
    return (
      <div className="panel" role="status">
        <p className="lead">{t("brain.ready", { name })}</p>
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            autoFocus
            onClick={onDone}
          >
            {t("common.back")}
          </button>
        </div>
      </div>
    );
  }
  if (state.phase === "failed") {
    return (
      <div className="panel panel-bad" role="alert">
        <p className="lead">{t("brain.failed", { name })}</p>
        {state.kind === "rejected" && state.message !== "" ? (
          <p>{state.message}</p>
        ) : null}
        {state.fix !== "" ? <p>{state.fix}</p> : null}
        <p className="code">{t("common.code", { code: state.code })}</p>
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            autoFocus
            onClick={onDone}
          >
            {t("common.back")}
          </button>
        </div>
      </div>
    );
  }
  return null;
}

/** Switch brain (survival-portal.md section 4.1.9). */
export function Brain({ status }: { status: PortalStatus }) {
  useLang();
  const [selected, setSelected] = useState<BrainChoice | null>(null);
  const choices = brainChoices(status.brain.choices);
  const running = status.brain.loaded.map((m) => m.display_name).join(", ");

  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("brain.title")}
      </h1>
      {running === "" ? null : (
        <p className="lead">{t("brain.runningNow", { name: running })}</p>
      )}
      {selected ? (
        <Switcher
          key={selected.id}
          choice={selected}
          status={status}
          onDone={() => setSelected(null)}
        />
      ) : choices.length === 0 ? (
        <p className="lead">{t("brain.none")}</p>
      ) : (
        <ul className="choices">
          {choices.map((c) => {
            const isOn = isRunning(c, status);
            const speed = speedOf(c, status.alerts);
            return (
              <li key={c.id} className="choice">
                <div className="choice-main">
                  <span className="choice-name">
                    {`${isOn ? "●" : "○"} ${c.display_name}`}
                  </span>
                  {c.vision ? (
                    <span className="tag">{t("brain.seesPhotos")}</span>
                  ) : null}
                  <span className="block">{c.summary}</span>
                  {speed ? (
                    <span className="block">
                      {t(`brain.speed.${speed}` as Key)}
                    </span>
                  ) : null}
                </div>
                <button
                  type="button"
                  className={isOn ? "btn" : "btn btn-primary"}
                  disabled={isOn}
                  onClick={() => setSelected(c)}
                >
                  {isOn ? t("brain.running") : t("brain.use")}
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <p>{t("brain.note")}</p>
      <div className="actions">
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
