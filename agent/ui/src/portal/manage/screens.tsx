import type { ReactNode } from "react";
import type { ManageScreen } from "../router";
import type { PortalStatus } from "../status";
import { ContentScreen } from "./ContentScreen";
import { HealthScreen } from "./HealthScreen";
import { ModeScreen } from "./ModeScreen";
import { ModelsScreen } from "./ModelsScreen";
import { SystemScreen } from "./SystemScreen";

/** The body of a Manage screen (dashboard.md section 3.1); the operator-session gate is in view.ts. */
export function manageBody(screen: ManageScreen, status: PortalStatus): ReactNode {
  switch (screen) {
    case "manage/mode":
      return <ModeScreen status={status} />;
    case "manage/content":
      return <ContentScreen status={status} />;
    case "manage/models":
      return <ModelsScreen status={status} />;
    case "manage/health":
      return <HealthScreen status={status} />;
    case "manage/system":
      return <SystemScreen status={status} />;
  }
}
