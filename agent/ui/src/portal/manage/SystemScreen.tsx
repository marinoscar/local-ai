import { t } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ManageFrame } from "./ManageFrame";

/** The System screen (dashboard.md section 4, issue #500). Placeholder until that issue lands. */
export function SystemScreen({ status }: { status: PortalStatus }) {
  void status;
  return (
    <ManageFrame title={t("system.title")} lead={t("common.notReady")}>
      {null}
    </ManageFrame>
  );
}
