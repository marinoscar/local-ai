import "../styles/status-brain.css";
import { isFixAction } from "../actions";
import { FixButton } from "../components/FixButton";
import { clockText, durationText } from "../format";
import { t, useLang, type Key } from "../i18n/i18n";
import { navigate } from "../router";
import {
  batteryLevel,
  clientErrors,
  type CapName,
  type Capability,
  type PortalStatus,
} from "../status";

const CAPS: readonly CapName[] = [
  "chat",
  "photo",
  "talk",
  "library",
  "maps",
  "manuals",
];

const MARK = { ready: "✓", degraded: "!", unavailable: "✗" } as const;

function ok(v: boolean | undefined): string {
  return v ? "✓" : "✗";
}

function capDetail(cap: CapName, c: Capability): string {
  if (cap === "library" && Array.isArray(c.books))
    return t("status.books", { n: c.books.length });
  if (cap === "maps" && c.state !== "unavailable")
    return t("status.mapsDetail", { search: ok(c.search), gps: ok(c.gps) });
  return c.reason ?? "";
}

function Battery({ power }: { power: PortalStatus["power"] }) {
  const lvl = batteryLevel(power);
  const pct = power.battery_pct;
  if (pct === null) {
    return (
      <p className="lead">{power.on_ac ? t("status.onAc") : t("btn.status")}</p>
    );
  }
  return (
    <>
      <div className="meter-row">
        <progress
          className={`meter meter-${lvl}`}
          max={100}
          value={pct}
          aria-label={t("status.battery")}
        />
        <span className={`meter-text battery-${lvl}`}>
          {`${pct} %${lvl === "critical" ? " ✗" : lvl === "low" ? " !" : ""}`}
        </span>
      </div>
      <p className="lead">
        {power.on_ac || power.minutes_left === null
          ? t("status.onAc")
          : t("status.onBattery", { time: durationText(power.minutes_left) })}
      </p>
    </>
  );
}

/** Status and battery (survival-portal.md section 4.1.8). It has no "Leave survival mode" button. */
export function Status({ status }: { status: PortalStatus }) {
  useLang();
  const brain = status.brain.loaded.map((m) => m.display_name).join(", ");
  const errors = clientErrors();
  return (
    <section className="screen screen-wide" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("status.title")}
      </h1>
      <div className="status-grid">
        <div className="status-col">
          <h2 className="status-h">{t("status.battery")}</h2>
          <Battery power={status.power} />
          <p>{t("status.powerTip")}</p>
          <dl className="facts">
            <dt>{t("btn.brain")}</dt>
            <dd>
              {brain === ""
                ? t("status.noBrain")
                : `${brain} (${t("brain.running")})`}
            </dd>
            <dt>{t("status.mode")}</dt>
            <dd>
              {status.mode === "survival"
                ? t("status.mode.survival")
                : status.mode}
            </dd>
            {status.ark ? (
              <>
                <dt>{t("status.ark")}</dt>
                <dd>
                  <span className={`ark-dot ark-${status.ark.overall}`}>
                    {"●"}
                  </span>
                  <span>{t(`status.ark.${status.ark.overall}` as Key)}</span>
                  {status.ark.drive_connected ? (
                    <span className="block">{t("status.drive")}</span>
                  ) : null}
                </dd>
              </>
            ) : null}
            <dt>{t("status.clock")}</dt>
            <dd>
              {clockText(status.generated_at)}
              {status.clock_ok ? null : (
                <span className="block warn-text">
                  {`! ${t("status.clockWrong")}`}
                </span>
              )}
            </dd>
          </dl>
        </div>
        <div className="status-col">
          <h2 className="status-h">{t("status.works")}</h2>
          <ul className="works">
            {CAPS.map((cap) => {
              const c = status.capabilities[cap];
              if (!c) return null;
              const detail = capDetail(cap, c);
              return (
                <li key={cap} className={`work work-${c.state}`}>
                  <span className="mark" aria-hidden="true">
                    {MARK[c.state]}
                  </span>
                  <span className="work-name">{t(`btn.${cap}` as Key)}</span>
                  <span className="sr-only">
                    {t(`status.cap.${c.state}` as Key)}
                  </span>
                  {detail === "" ? null : (
                    <span className="work-detail">{detail}</span>
                  )}
                </li>
              );
            })}
          </ul>
          <h2 className="status-h">{t("status.problems")}</h2>
          {status.alerts.length === 0 ? (
            <p>{t("status.noProblems")}</p>
          ) : (
            <ul className="problems">
              {status.alerts.map((a) => (
                <li key={a.code} className={`problem problem-${a.severity}`}>
                  <span className="code">{a.code}</span>
                  <span className="block">{a.title}</span>
                  <span className="block">{a.fix}</span>
                  {isFixAction(a.action) ? (
                    <FixButton action={a.action} />
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      <details className="details">
        <summary>{t("status.details")}</summary>
        <pre className="details-log">
          {errors.length === 0 ? t("status.noErrors") : errors.join("\n")}
        </pre>
      </details>
      <div className="actions">
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
