import { t, useLang, type Key } from "../i18n/i18n";
import { CAN, EMERGENCY, EXPERT, TASKS } from "../manage/guide";
import { parseInline } from "../manuals/inline";
import { navigate } from "../router";
import type { PortalStatus } from "../status";
import "../styles/manuals.css";

function Inline({ text }: { text: string }) {
  return (
    <>
      {parseInline(text).map((s, i) =>
        s.kind === "strong" ? (
          <strong key={i}>{s.text}</strong>
        ) : s.kind === "em" ? (
          <em key={i}>{s.text}</em>
        ) : s.kind === "code" ? (
          <code key={i}>{s.text}</code>
        ) : (
          <span key={i}>{s.text}</span>
        ),
      )}
    </>
  );
}

const line = (k: Key) => <Inline text={t(k)} />;

/**
 * Help and the guide (dashboard.md section 4.7; survival-portal.md section 4.8): what to do in an
 * emergency, what each button does, the common tasks step by step with the exact button names, the
 * manuals, and the typed commands only under "For an expert". Pure SPA content: it fetches nothing,
 * so it works with no content on disk and with the agent down. The emergency number comes from the
 * last status, else 911.
 */
export function Help({ status }: { status: PortalStatus | null }) {
  useLang();
  const n = status?.emergency_number || "911";
  const manuals = status?.capabilities.manuals?.state !== "unavailable" && status !== null;
  return (
    <section className="screen screen-wide guide" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("help.title")}
      </h1>
      <p className="lead">{t("guide.lead")}</p>

      <h2 className="guide-h">{t("guide.emergency.title")}</h2>
      <ol className="help-steps">
        {EMERGENCY.map((k) => (
          <li key={k} className="help-step">
            {line(k)}
          </li>
        ))}
      </ol>
      <p className="help-emergency">{t("help.emergency", { n })}</p>
      <p className="help-last">{t("help.lastResort")}</p>

      <h2 className="guide-h">{t("guide.can.title")}</h2>
      <ul className="guide-can">
        {CAN.map((k) => (
          <li key={k}>{line(k)}</li>
        ))}
      </ul>

      <h2 className="guide-h">{t("guide.tasks.title")}</h2>
      <div className="guide-tasks">
        {TASKS.map((task) => (
          <details key={task.id} className="card guide-task">
            <summary className="guide-task-title">{t(task.title)}</summary>
            <ol>
              {task.steps.map((k) => (
                <li key={k}>{line(k)}</li>
              ))}
            </ol>
          </details>
        ))}
      </div>

      <h2 className="guide-h">{t("guide.manuals.title")}</h2>
      <p>{t("guide.manuals.lead")}</p>
      {manuals ? (
        <button type="button" className="btn" onClick={() => navigate("manuals")}>
          {t("guide.manuals.open")}
        </button>
      ) : null}
      <p>{line("help.codes")}</p>

      <details className="expert">
        <summary>{t("guide.expert.title")}</summary>
        <p>{t("guide.expert.lead")}</p>
        <dl className="guide-expert">
          {EXPERT.map(([k, cmd]) => (
            <div key={cmd}>
              <dt>{t(k)}</dt>
              <dd>
                <code>{cmd}</code>
              </dd>
            </div>
          ))}
        </dl>
        <p>{t("guide.expert.library")}</p>
      </details>

      <div className="actions">
        <button type="button" className="btn btn-primary" onClick={() => navigate("")}>
          {t("guide.back")}
        </button>
      </div>
    </section>
  );
}
