import { useEffect, useState, type ReactNode } from "react";
import { CODE_LOST_CONTACT } from "../api";
import { t, useLang, type Key } from "../i18n/i18n";
import { batteryLevel, type PortalStatus, type StatusState } from "../status";
import { Banner } from "./Banner";
import { LangToggle } from "./LangToggle";
import { LiveRegion } from "./LiveRegion";
import { StatusLine } from "./StatusLine";
import { TextScale } from "./TextScale";
import { useStartSurvival } from "./useStartSurvival";

/** The header names the mode (dashboard.md section 4.1); survival keeps the Portal's own title. */
const BRAND_KEY: Record<string, Key> = {
  online: "home.mode.online",
  offline: "home.mode.offline",
};

function useClock(): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const h = window.setInterval(() => setNow(new Date()), 15000);
    return () => window.clearInterval(h);
  }, []);
  return now;
}

function Battery({ power }: { power: PortalStatus["power"] }) {
  if (power.battery_pct === null) return null;
  const lvl = batteryLevel(power);
  const glyph = lvl === "critical" ? "✗" : lvl === "low" ? "!" : "";
  return (
    <span
      className={`battery battery-${lvl}`}
      role="img"
      aria-label={`${t("status.battery")} ${power.battery_pct} %`}
    >
      <span aria-hidden="true">{`${power.on_ac ? "⚡ " : ""}${power.battery_pct} % ${glyph}`}</span>
    </span>
  );
}

function Banners({ state }: { state: StatusState }) {
  const { busy, failed, start } = useStartSurvival();
  const status = state.status;
  return (
    <>
      {state.lost ? (
        <Banner kind="danger" assertive text={t("banner.lostContact")}>
          <span className="banner-note">
            {`${t("banner.lostContactFix")} ${t("common.code", { code: CODE_LOST_CONTACT })}`}
          </span>
        </Banner>
      ) : null}
      {status && status.mode !== "survival" ? (
        <Banner kind="info" text={t("banner.survivalOff")}>
          <button
            type="button"
            className="btn btn-primary"
            disabled={busy}
            onClick={() => void start()}
          >
            {t("banner.startSurvival")}
          </button>
          {failed ? (
            <span className="banner-note">{t("banner.startFailed")}</span>
          ) : null}
        </Banner>
      ) : null}
      {status && batteryLevel(status.power) === "critical" ? (
        <Banner kind="danger" assertive text={t("banner.batteryCritical")} />
      ) : null}
      {status && batteryLevel(status.power) === "low" ? (
        <Banner
          kind="warn"
          text={t("banner.batteryLow", { pct: status.power.battery_pct ?? "" })}
        />
      ) : null}
    </>
  );
}

/** The frame of every screen: skip link, header, banners, main landmark, live regions. */
export function Shell({
  state,
  home,
  children,
}: {
  state: StatusState;
  home: boolean;
  children: ReactNode;
}) {
  const lang = useLang();
  const now = useClock();
  const status = state.status;
  const time = now.toLocaleTimeString(lang, {
    hour: "2-digit",
    minute: "2-digit",
  });
  const Brand = home ? "h1" : "p";
  return (
    <>
      <a className="skip" href="#main">
        {t("common.skip")}
      </a>
      <header className="top">
        <div className="top-row">
          <Brand className="brand">
            {t((status && BRAND_KEY[status.mode]) || "app.title")}
          </Brand>
          <div className="top-tools">
            <LangToggle />
            <TextScale />
            <span className="clock" aria-label={t("status.clock")}>
              {time}
            </span>
            {status ? <Battery power={status.power} /> : null}
          </div>
        </div>
        {status ? <StatusLine status={status} home={home} /> : null}
        {!status ? (
          <div className="statusline">{t("common.loading")}</div>
        ) : null}
      </header>
      <Banners state={state} />
      <main id="main" tabIndex={-1} className="main">
        {children}
      </main>
      <LiveRegion />
    </>
  );
}
