import { setLang, t, useLang, type Lang } from "../i18n/i18n";

// Language names are shown in their own language on purpose (never translated).
const OPTIONS: { lang: Lang; label: string }[] = [
  { lang: "en", label: "English" },
  { lang: "es", label: "Español" },
];

export function LangToggle() {
  const cur = useLang();
  return (
    <div className="seg" role="group" aria-label={t("talk.language")}>
      {OPTIONS.map((o) => (
        <button
          key={o.lang}
          type="button"
          className="seg-btn"
          lang={o.lang}
          aria-pressed={cur === o.lang}
          onClick={() => setLang(o.lang)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
