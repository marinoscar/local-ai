import { getLang, t } from "./i18n/i18n";
import type { PortalStatus } from "./status";

/** durationText: 145 -> "2 h 25 min", 45 -> "45 min". */
export function durationText(minutes: number): string {
  const m = Math.max(0, Math.round(minutes));
  const h = Math.floor(m / 60);
  return h > 0 ? t("time.hm", { h, m: m % 60 }) : t("time.m", { m });
}

/** clockText: "Mon, 2 Nov 2026, 14:05" in the UI language (the node clock, from the status). */
export function clockText(iso: string | undefined, lang = getLang()): string {
  const d = iso ? new Date(iso) : new Date();
  if (Number.isNaN(d.getTime())) return "";
  return new Intl.DateTimeFormat(lang, {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(d);
}

/** powerText is the subtitle of the Status and battery button: "72 %, 2 h 25 min". */
export function powerText(p: PortalStatus["power"] | undefined): string {
  if (!p || p.battery_pct === null)
    return p?.on_ac ? t("status.onAc") : t("btn.status");
  const pct = `${p.battery_pct} %`;
  return !p.on_ac && p.minutes_left !== null
    ? `${pct}, ${durationText(p.minutes_left)}`
    : pct;
}
