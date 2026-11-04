import { t, type Key } from "../i18n/i18n";
import { parseInline } from "../manuals/inline";
import { navigate } from "../router";
import type { PortalStatus } from "../status";
import "../styles/manuals.css";

const STEPS: Key[] = ["help.survival", "help.drive", "help.library", "help.codes"];

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

/**
 * The built-in emergency page (survival-portal.md section 4.8). Pure SPA content from the i18n files:
 * it fetches nothing, so it works with no content on disk and with the agent down. The emergency
 * number comes from the last status, else 911.
 */
export function Help({ status }: { status: PortalStatus | null }) {
  const n = status?.emergency_number || "911";
  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("help.title")}
      </h1>
      <ol className="help-steps">
        {STEPS.map((k) => (
          <li key={k} className="help-step">
            <Inline text={t(k)} />
          </li>
        ))}
      </ol>
      <p className="help-emergency">{t("help.emergency", { n })}</p>
      <p className="help-last">{t("help.lastResort")}</p>
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          onClick={() => navigate("")}
        >
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
