import type { ReactNode } from "react";
import { LockIcon } from "../icons/Icons";

export interface BigButtonProps {
  num: number;
  icon: ReactNode;
  label: string;
  sub: string;
  /** Accent class (border and icon colour only). */
  accent: string;
  /** Shows the lock; the text says why, for screen readers. */
  lockHint?: string;
  /** A problem is known: a mark next to the label (never colour only). */
  mark?: string;
  onActivate: () => void;
}

/** One of the eight big buttons of Home (300x170 px at 100 %). */
export function BigButton({
  num,
  icon,
  label,
  sub,
  accent,
  lockHint,
  mark,
  onActivate,
}: BigButtonProps) {
  const id = `bb-${num}`;
  return (
    <>
      <button
        type="button"
        className={`big ${accent}`}
        aria-keyshortcuts={String(num)}
        aria-describedby={lockHint ? `${id}-l` : undefined}
        onClick={onActivate}
      >
        <span className="big-top">
          {icon}
          <span className="big-num" aria-hidden="true">
            {num}
          </span>
          {lockHint ? <LockIcon /> : null}
          {mark ? (
            <span className="big-mark" aria-hidden="true">
              {mark}
            </span>
          ) : null}
        </span>
        <span className="big-label">{label}</span>
        <span className="big-sub">{sub}</span>
      </button>
      {lockHint ? (
        <span className="sr-only" id={`${id}-l`}>
          {lockHint}
        </span>
      ) : null}
    </>
  );
}
