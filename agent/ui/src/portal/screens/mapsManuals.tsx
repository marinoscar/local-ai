import { useEffect, type ReactNode } from "react";
import type { Screen } from "../router";
import type { PortalStatus } from "../status";
import { Help } from "./Help";
import { Manuals } from "./Manuals";
import { Maps } from "./Maps";

/**
 * With no manuals on disk the Manuals button opens /portal/help: the address changes too (replace,
 * so Back does not bounce), which keeps a reload on the page the person sees.
 */
export function useHelpRedirect(
  screen: Screen | "",
  status: PortalStatus | null,
): void {
  const noDocs =
    screen === "manuals" && status?.capabilities.manuals?.state === "unavailable";
  useEffect(() => {
    if (!noDocs) return;
    window.history.replaceState(null, "", "/portal/help");
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, [noDocs]);
}

/** The Maps, Manuals and Help screens (PORTAL-10); null for every other screen. */
export function mapsManualsBody(
  screen: Screen,
  status: PortalStatus | null,
): ReactNode {
  switch (screen) {
    case "maps":
      return <Maps />;
    case "manuals":
      return <Manuals status={status} />;
    case "help":
      return <Help status={status} />;
    default:
      return null;
  }
}
