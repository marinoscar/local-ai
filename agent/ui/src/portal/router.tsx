import { useSyncExternalStore } from "react";

// A tiny router for the Portal's client routes (survival-portal.md section 3.1). The agent serves
// portal.html for every /portal/* path, so deep links and reloads work.

export const SCREENS = [
  "chat",
  "photo",
  "talk",
  "library",
  "maps",
  "manuals",
  "status",
  "brain",
  "first-aid",
  "help",
  // Manage this computer (dashboard.md section 3.1): operator session only.
  "manage/mode",
  "manage/content",
  "manage/models",
  "manage/health",
  "manage/system",
] as const;
export type Screen = (typeof SCREENS)[number];

/** The Manage screens, in the order of the Manage row (dashboard.md section 4.1). */
export const MANAGE_SCREENS = [
  "manage/mode",
  "manage/content",
  "manage/models",
  "manage/health",
  "manage/system",
] as const;
export type ManageScreen = (typeof MANAGE_SCREENS)[number];

export function isManage(s: Screen | ""): s is ManageScreen {
  return (MANAGE_SCREENS as readonly string[]).includes(s);
}

/** screenOf maps a location path to a screen name; '' is Home. Unknown paths are Home. */
export function screenOf(pathname: string): Screen | "" {
  const rest = pathname.replace(/^\/portal\/?/, "").replace(/\/+$/, "");
  return (SCREENS as readonly string[]).includes(rest) ? (rest as Screen) : "";
}

export function pathOf(screen: Screen | ""): string {
  return screen === "" ? "/portal" : `/portal/${screen}`;
}

const listeners = new Set<() => void>();
if (typeof window !== "undefined")
  window.addEventListener("popstate", () => listeners.forEach((l) => l()));

export function navigate(screen: Screen | ""): void {
  const p = pathOf(screen);
  if (window.location.pathname !== p) {
    window.history.pushState(null, "", p);
    listeners.forEach((l) => l());
  }
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useScreen(): Screen | "" {
  return screenOf(
    useSyncExternalStore(subscribe, () => window.location.pathname),
  );
}

/**
 * navigatePath pushes a full Portal path with a query (for /portal/read and /portal/library?q=).
 * Used by the reader frame and the Library screen; plain screens keep using navigate().
 */
export function navigatePath(path: string): void {
  const cur = window.location.pathname + window.location.search;
  if (cur !== path) {
    window.history.pushState(null, "", path);
    listeners.forEach((l) => l());
  }
}

/** useLocation is pathname + search, so a query-only change re-renders. */
export function useLocation(): string {
  return useSyncExternalStore(
    subscribe,
    () => window.location.pathname + window.location.search,
  );
}
