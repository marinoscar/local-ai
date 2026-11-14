import { useEffect, type ReactNode } from "react";
import { announce } from "./LiveRegion";

export type BannerKind = "info" | "warn" | "danger";

const GLYPH: Record<BannerKind, string> = { info: "i", warn: "!", danger: "✗" };

interface Props {
  kind: BannerKind;
  text: string;
  /** Errors are announced assertively; everything else politely. */
  assertive?: boolean;
  /** A look of its own on top of the kind (the survival-off banner is warm gold, ADR-0014). */
  tone?: string;
  children?: ReactNode;
}

/** A full-width banner. Colour is never the only signal: a glyph and the text say it too. */
export function Banner({ kind, text, assertive, tone, children }: Props) {
  useEffect(() => announce(text, assertive), [text, assertive]);
  return (
    <div className={`banner banner-${kind}${tone ? ` banner-${tone}` : ""}`}>
      <span className="glyph" aria-hidden="true">
        {GLYPH[kind]}
      </span>
      <span className="banner-text">{text}</span>
      {children}
    </div>
  );
}
