import { t } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ManageFrame } from "./ManageFrame";

/** The AI models screen (dashboard.md section 4, issue #498). Placeholder until that issue lands. */
export function ModelsScreen({ status }: { status: PortalStatus }) {
  void status;
  return (
    <ManageFrame title={t("models.title")} lead={t("common.notReady")}>
      {null}
    </ManageFrame>
  );
}
