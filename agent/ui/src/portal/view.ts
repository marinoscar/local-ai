import type { Screen } from "./router";
import type { CapName, PortalStatus } from "./status";

const CAP_OF: Partial<Record<Screen, CapName>> = {
  chat: "chat",
  photo: "photo",
  talk: "talk",
  library: "library",
  maps: "maps",
  manuals: "manuals",
};
const NEEDS_SESSION: ReadonlySet<Screen> = new Set([
  "chat",
  "photo",
  "talk",
  "brain",
]);

export type View =
  | { kind: "home" }
  | { kind: "lock" }
  | { kind: "unavailable"; cap: CapName }
  | { kind: "screen"; screen: Screen };

/**
 * viewFor decides what a route shows. Before any screen: the lock (session none) and the
 * unavailable explanation; a button is never hidden and never a dead end.
 */
export function viewFor(
  screen: Screen | "",
  status: PortalStatus | null,
): View {
  if (screen === "") return { kind: "home" };
  if (status && NEEDS_SESSION.has(screen) && status.session === "none")
    return { kind: "lock" };
  const cap = CAP_OF[screen];
  // No manuals on disk: the button opens the built-in emergency page (survival-portal.md section 4.8).
  if (screen === "manuals" && status?.capabilities.manuals?.state === "unavailable")
    return { kind: "screen", screen: "help" };
  if (status && cap && status.capabilities[cap]?.state === "unavailable")
    return { kind: "unavailable", cap };
  return { kind: "screen", screen };
}
