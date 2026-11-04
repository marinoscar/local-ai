import { useEffect, type ReactNode } from "react";
import { announce } from "./LiveRegion";

export type BannerKind = "info" | "warn" | "danger";

const GLYPH: Record<BannerKind, string> = { info: "i", warn: "!", danger: "✗" };

interface Props {
  kind: BannerKind;
  text: string;
  /** Errors are announced assertively; everything else politely. */
  assertive?: boolean;
  children?: ReactNode;
}

/** A full-width banner. Colour is never the only signal: a glyph and the text say it too. */
export function Banner({ kind, text, assertive, children }: Props) {
  useEffect(() => announce(text, assertive), [text, assertive]);
  return (
    <div className={`banner banner-${kind}`}>
      <span className="glyph" aria-hidden="true">
        {GLYPH[kind]}
      </span>
      <span className="banner-text">{text}</span>
      {children}
    </div>
  );
}
