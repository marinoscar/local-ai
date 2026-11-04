import { useSyncExternalStore } from "react";

// Text scale (survival-portal.md section 4.10): three steps, remembered in localStorage.

export const SCALE_KEY = "lai.portal.textScale";
export const SCALES = [1, 1.25, 1.5] as const;
export type Scale = (typeof SCALES)[number];

export function parseScale(raw: string | null): Scale {
  const n = Number(raw);
  return (SCALES as readonly number[]).includes(n) ? (n as Scale) : 1;
}

/** stepScale moves one step up (+1) or down (-1) and stops at the ends. */
export function stepScale(cur: Scale, dir: 1 | -1): Scale {
  const i = SCALES.indexOf(cur) + dir;
  return SCALES[Math.min(SCALES.length - 1, Math.max(0, i))];
}

let scale: Scale = 1;
const listeners = new Set<() => void>();

function apply() {
  const root = document.documentElement;
  for (const s of SCALES)
    root.classList.toggle(`scale-${String(s).replace(".", "_")}`, s === scale);
}

export function initScale(): void {
  try {
    scale = parseScale(window.localStorage.getItem(SCALE_KEY));
  } catch {
    scale = 1;
  }
  apply();
}

export function getScale(): Scale {
  return scale;
}

export function setScale(s: Scale): void {
  scale = s;
  try {
    window.localStorage.setItem(SCALE_KEY, String(s));
  } catch {
    // Storage blocked: the choice lasts until reload.
  }
  apply();
  for (const l of listeners) l();
}

export function useScale(): Scale {
  return useSyncExternalStore((l) => {
    listeners.add(l);
    return () => listeners.delete(l);
  }, getScale);
}
