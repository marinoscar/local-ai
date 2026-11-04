import type { Alert, BrainChoice, PortalStatus } from "./status";

/** The code of "no graphics card in use; answers are slow" (survival-portal.md section 4.1.9). */
export const CODE_NO_GPU = "LAI-SVC-113";

/** brainChoices lists the loadable models, survival models first, then by display name. */
export function brainChoices(
  choices: readonly BrainChoice[] | undefined,
): BrainChoice[] {
  return (choices ?? [])
    .filter((c) => c.loadable)
    .sort(
      (a, b) =>
        Number(b.survival) - Number(a.survival) ||
        a.display_name.localeCompare(b.display_name),
    );
}

/** speedOf is the speed hint to show; on a node without a graphics card everything is slow. */
export function speedOf(
  c: BrainChoice,
  alerts: readonly Alert[],
): "fast" | "medium" | "slow" | null {
  if (alerts.some((a) => a.code === CODE_NO_GPU)) return "slow";
  return c.speed_hint ?? null;
}

export function isRunning(c: BrainChoice, s: PortalStatus): boolean {
  return s.brain.loaded.some((m) => m.id === c.id);
}
