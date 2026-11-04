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
] as const;
export type Screen = (typeof SCREENS)[number];

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
