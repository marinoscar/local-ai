import { t } from "../i18n/i18n";
import { SCALES, setScale, useScale } from "../prefs";

// A, A+, A++ are symbols, not words.
const LABELS = ["A", "A+", "A++"];

export function TextScale() {
  const cur = useScale();
  return (
    <div className="seg" role="group" aria-label={t("size.label")}>
      {SCALES.map((s, i) => (
        <button
          key={s}
          type="button"
          className="seg-btn"
          aria-pressed={cur === s}
          aria-label={`${t("size.label")} ${Math.round(s * 100)} %`}
          onClick={() => setScale(s)}
        >
          {LABELS[i]}
        </button>
      ))}
    </div>
  );
}
