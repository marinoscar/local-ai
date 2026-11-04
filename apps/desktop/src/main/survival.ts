// Survival UX: the full-screen Portal behaviour.
// Scaffold stub (DSK-01): the real module arrives with DSK-09. Spec: docs/specs/desktop.md section 3.1.
export const MODULE = 'survival' as const;

/**
 * The hook the start-up sequence calls when the node is in survival mode (desktop.md 4.1). DSK-03 only calls it; the
 * content view is already loading `/portal`. DSK-09 replaces the body: show and focus the window, full screen, the
 * collapsed top bar.
 */
export function enter(): void {
  // nothing until DSK-09
}
