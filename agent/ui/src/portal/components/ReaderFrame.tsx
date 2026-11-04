import { useCallback, useEffect, useRef } from "react";
import { t } from "../i18n/i18n";
import { isLibraryBack, safeBack, safeReaderSrc } from "../library/reader";
import { useScale } from "../prefs";
import { navigate, navigatePath } from "../router";
import { TextScale } from "./TextScale";

/**
 * The reader frame (survival-portal.md section 4.1.5): a Portal header bar above a same-origin
 * iframe. Used by Library, Maps, Manuals and First aid. Esc always goes home, also while the focus
 * is inside the frame (same-origin, so the frame's document gets the same key handler).
 */
export function ReaderFrame({ src, back }: { src: string; back: string }) {
  const ref = useRef<HTMLIFrameElement>(null);
  const scale = useScale();
  const safe = safeReaderSrc(src);
  const backTo = safeBack(back);

  // The frame's document is replaced on every navigation inside it, so the handler and the text
  // size are applied again after each load. Writes go through the CSSOM, which the Portal CSP allows.
  const applyToFrame = useCallback(() => {
    let doc: Document | null | undefined;
    try {
      doc = ref.current?.contentDocument;
    } catch {
      return;
    }
    if (!doc || !doc.documentElement) return;
    doc.documentElement.style.fontSize = `${Math.round(scale * 100)}%`;
  }, [scale]);

  const onKey = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape") navigate("");
  }, []);

  const onLoad = useCallback(() => {
    let doc: Document | null | undefined;
    try {
      doc = ref.current?.contentDocument;
    } catch {
      return;
    }
    if (!doc) return;
    doc.removeEventListener("keydown", onKey);
    doc.addEventListener("keydown", onKey);
    applyToFrame();
  }, [applyToFrame, onKey]);

  useEffect(applyToFrame, [applyToFrame]);

  // Move the focus into the header so a keyboard user can reach Back and Home first.
  useEffect(() => {
    document.getElementById("reader-back")?.focus({ preventScroll: true });
  }, [src]);

  // Not an allowed path: never load it, go home.
  useEffect(() => {
    if (!safe) navigate("");
  }, [safe]);
  if (!safe) return null;
  return (
    <section className="reader" aria-label={t("reader.frame")}>
      <div className="reader-bar" role="toolbar" aria-label={t("reader.frame")}>
        <button
          id="reader-back"
          type="button"
          className="btn"
          onClick={() => navigatePath(backTo)}
        >
          {`◄ ${t(isLibraryBack(backTo) ? "library.backToResults" : "common.back")}`}
        </button>
        <button type="button" className="btn" onClick={() => navigate("")}>
          {`⌂ ${t("common.home")}`}
        </button>
        <TextScale />
      </div>
      <iframe
        ref={ref}
        className="reader-frame"
        title={t("reader.frame")}
        src={safe}
        onLoad={onLoad}
      />
    </section>
  );
}
