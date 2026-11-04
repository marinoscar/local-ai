import { ReaderFrame } from "../components/ReaderFrame";

/**
 * Maps (button 5): the offline map viewer in the reader frame, so Esc and Home are always there
 * (survival-portal.md section 4.1.6). The search, Where am I and Nearest controls live in the viewer.
 * When no map is installed the router shows the unavailable screen before this one.
 */
export function Maps() {
  return <ReaderFrame src="/maps/" back="/portal" />;
}
