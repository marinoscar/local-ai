import { t } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ManageFrame } from "./ManageFrame";

/** The Mode screen (dashboard.md section 4, issue #494). Placeholder until that issue lands. */
export function ModeScreen({ status }: { status: PortalStatus }) {
  void status;
  return (
    <ManageFrame title={t("mode.title")} lead={t("common.notReady")}>
      {null}
    </ManageFrame>
  );
}
