import { apiSend } from "../api";
import type { Key } from "../i18n/i18n";

// The Mode screen's data (dashboard.md section 4.2; agent.md section 3.8.15).

export type ModeName = "online" | "offline" | "survival";
export const MODES: readonly ModeName[] = ["online", "offline", "survival"];

export interface ModeView {
  mode: string;
  since?: string | null;
  transition: {
    op_id: string;
    from: string;
    target: string;
    step?: string;
    warnings?: { code: string; message: string }[];
  } | null;
  allowed_targets?: string[];
  leave_requires_admin?: boolean;
  connectivity?: string;
  suggest_survival?: boolean;
}

export function isMode(v: unknown): v is ModeName {
  return v === "online" || v === "offline" || v === "survival";
}

export const MODE_LABEL: Record<ModeName, Key> = {
  online: "mode.online",
  offline: "mode.offline",
  survival: "mode.survival",
};

export const MODE_WHAT: Record<ModeName, Key> = {
  online: "mode.online.what",
  offline: "mode.offline.what",
  survival: "mode.survival.what",
};

export const MODE_CONFIRM: Record<ModeName, Key> = {
  online: "mode.confirm.online",
  offline: "mode.confirm.offline",
  survival: "mode.confirm.survival",
};

/**
 * leaveDefault is the destination the Leave survival dialog preselects: Online when the agent has
 * seen the internet, Offline otherwise (no internet, limited, or not checked yet).
 */
export function leaveDefault(connectivity: string | undefined): "online" | "offline" {
  return connectivity === "full" ? "online" : "offline";
}

export function getMode(send: typeof apiSend = apiSend): Promise<ModeView | null> {
  return send<ModeView>("GET", "/mode");
}

/**
 * changeMode is the button: survival uses POST /mode/survival (never refused, queued while the agent
 * starts); the others POST /mode {target}. An operator leaves survival without the PIN (ADR-0011).
 */
export function changeMode(
  target: ModeName,
  send: typeof apiSend = apiSend,
): Promise<unknown> {
  return target === "survival"
    ? send("POST", "/mode/survival", {})
    : send("POST", "/mode", { target });
}
