import { useCallback, useState } from "react";
import { startSurvival } from "../api";
import { recordError, refreshStatus } from "../status";

/** The "Start survival mode" action shared by the banner and the lock dialog. */
export function useStartSurvival() {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const start = useCallback(async () => {
    setBusy(true);
    setFailed(false);
    try {
      await startSurvival();
      await refreshStatus();
    } catch (e) {
      recordError(e);
      setFailed(true);
    } finally {
      setBusy(false);
    }
  }, []);
  return { busy, failed, start };
}
