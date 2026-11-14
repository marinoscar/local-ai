import { t } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ManageFrame } from "./ManageFrame";

/** The Health screen (dashboard.md section 4, issue #499). Placeholder until that issue lands. */
export function HealthScreen({ status }: { status: PortalStatus }) {
  void status;
  return (
    <ManageFrame title={t("health.title")} lead={t("common.notReady")}>
      {null}
    </ManageFrame>
  );
}
