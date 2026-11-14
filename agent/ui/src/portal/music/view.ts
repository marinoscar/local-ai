import type { MusicStatus } from "@localai/ui/music";
import type { PortalStatus } from "../status";

// What the /music/ page shows first (issue #512, ADR-0009 "state first"). Plain data so it is tested without a
// browser; screens/Music.tsx renders it.

export const CODE_NOT_INSTALLED = "LAI-SVC-150";
export const CODE_NOT_LOADED = "LAI-SVC-151";
export const CODE_ENGINE_FAILED = "LAI-LLM-040";
export const CODE_SURVIVAL = "LAI-LLM-053";
export const CODE_SIGN_IN = "LAI-SEC-001";

export type MusicView =
  /** Waiting for the first answers. */
  | { kind: "loading" }
  /** Music never runs in survival mode (contract section 5). */
  | { kind: "survival" }
  /** The music routes need the computer's own app or an operator sign-in (auth `op`). */
  | { kind: "sign_in" }
  /** GET /music/status failed. */
  | { kind: "error"; code: string; message: string; fix: string }
  /** The runtime is not on this computer: explain, LAI-SVC-150. */
  | { kind: "not_installed" }
  /** Installed, no music model running: [Switch to Music]. */
  | { kind: "not_loaded" }
  /** The engine is starting or stopping: wait. */
  | { kind: "starting" }
  | { kind: "stopping" }
  /** The engine failed: LAI-LLM-040 and [Switch to Music] again. */
  | { kind: "failed" }
  /** Ready: the studio. */
  | { kind: "ready" };

export interface MusicStatusState {
  status: MusicStatus | null;
  error: { code: string; message: string; fix: string } | null;
}

export function musicView(
  portal: PortalStatus | null,
  music: MusicStatusState,
): MusicView {
  if (!portal) return { kind: "loading" };
  if (portal.mode === "survival") return { kind: "survival" };
  if (portal.session !== "full") return { kind: "sign_in" };
  const s = music.status;
  if (!s) return music.error ? { kind: "error", ...music.error } : { kind: "loading" };
  if (!s.installed) return { kind: "not_installed" };
  switch (s.engine_state) {
    case "ready":
      return s.loaded_model ? { kind: "ready" } : { kind: "not_loaded" };
    case "starting":
      return { kind: "starting" };
    case "stopping":
      return { kind: "stopping" };
    case "failed":
      return { kind: "failed" };
    default:
      return { kind: "not_loaded" };
  }
}

/** The song list is useful whenever the songs can be read: songs are files, the engine is not needed to play them. */
export function showLibrary(view: MusicView): boolean {
  return (
    view.kind === "ready" ||
    view.kind === "not_loaded" ||
    view.kind === "starting" ||
    view.kind === "stopping" ||
    view.kind === "failed"
  );
}

/** The studio form is shown (disabled) while music is on its way, so a person can write the lyrics meanwhile. */
export function showStudio(view: MusicView): boolean {
  return view.kind === "ready" || view.kind === "starting";
}
