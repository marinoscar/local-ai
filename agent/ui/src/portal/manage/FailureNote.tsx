import type { Failure } from "../api";
import { t } from "../i18n/i18n";

/** A failed button in plain words: what happened, the one-sentence fix, the doctor code (AGENTS.md rule 4). */
export function FailureNote({ failure }: { failure: Failure }) {
  return (
    <div className="fix-result fix-failed" role="alert">
      <p>{t("manage.failed", { message: failure.message })}</p>
      {failure.fix === "" ? null : <p>{failure.fix}</p>}
      {failure.code === "" ? null : (
        <p className="code">{t("common.code", { code: failure.code })}</p>
      )}
    </div>
  );
}
