import { t } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ManageFrame } from "./ManageFrame";

/** The Offline content and drives screen (dashboard.md section 4, issue #496). Placeholder until that issue lands. */
export function ContentScreen({ status }: { status: PortalStatus }) {
  void status;
  return (
    <ManageFrame title={t("content.title")} lead={t("common.notReady")}>
      {null}
    </ManageFrame>
  );
}
