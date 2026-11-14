import { useCallback, useEffect, useState } from "react";
import { toErrorInfo, type MusicTransport } from "@localai/ui/music";
import { CODE_LOST_CONTACT } from "../api";
import type { MusicStatusState } from "./view";

/** How often the page re-reads GET /music/status while it is open (an engine that starts or stops shows up). */
export const MUSIC_POLL_MS = 5000;

/** useMusicStatus polls the music status while `enabled` (a full session outside survival). */
export function useMusicStatus(
  transport: Pick<MusicTransport, "status">,
  enabled: boolean,
): MusicStatusState & { refetch: () => Promise<void> } {
  const [state, setState] = useState<MusicStatusState>({
    status: null,
    error: null,
  });

  const refetch = useCallback(async () => {
    try {
      const status = await transport.status();
      setState({ status, error: null });
    } catch (e) {
      const info = toErrorInfo(e, CODE_LOST_CONTACT);
      setState((s) => ({
        status: s.status,
        error: { code: info.code, message: info.message, fix: info.fix ?? "" },
      }));
    }
  }, [transport]);

  useEffect(() => {
    if (!enabled) return;
    void refetch();
    const h = window.setInterval(() => void refetch(), MUSIC_POLL_MS);
    return () => window.clearInterval(h);
  }, [enabled, refetch]);

  return { ...state, refetch };
}
