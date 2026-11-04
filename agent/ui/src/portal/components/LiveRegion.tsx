import { useSyncExternalStore } from "react";

// The aria-live regions of the Portal (survival-portal.md section 4.10): polite for status changes,
// assertive for errors. Components call announce(); the text is read once by a screen reader.

interface Said {
  polite: string;
  assertive: string;
}

let said: Said = { polite: "", assertive: "" };
const listeners = new Set<() => void>();

export function announce(text: string, assertive = false): void {
  const key = assertive ? "assertive" : "polite";
  if (said[key] === text) return;
  said = { ...said, [key]: text };
  listeners.forEach((l) => l());
}

export function LiveRegion() {
  const s = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => said,
  );
  return (
    <div className="sr-only">
      <div aria-live="polite" role="status">
        {s.polite}
      </div>
      <div aria-live="assertive" role="alert">
        {s.assertive}
      </div>
    </div>
  );
}
