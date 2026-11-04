// The resolved web slice manifest: the slices this build mounts, and what they
// contribute to the shared parts of the app (the two settings registries, the
// routes, the providers, the shell slots). `ENABLED_SLICES` is validated by
// `@app/shared`, the file the API reads too.
import { ENABLED_SLICES, type SliceId } from '@app/shared';

import { ALL_WEB_SLICES } from './definitions';
import type { WebSlice } from './slice';

/** The enabled slices, in mount order. */
export const ENABLED_WEB: readonly WebSlice[] = ENABLED_SLICES.map((id) => ALL_WEB_SLICES[id]);

/** Is this optional slice mounted? */
export function isSliceEnabled(id: SliceId): boolean {
  return ENABLED_SLICES.includes(id);
}

const pick = <K extends keyof WebSlice>(key: K): NonNullable<WebSlice[K]> extends readonly (infer T)[] ? T[] : never =>
  ENABLED_WEB.flatMap((slice) => (slice[key] ?? []) as readonly unknown[]) as never;

export const sliceRoutes = pick('routes');
export const slicePublicRoutes = pick('publicRoutes');
export const sliceAdminCards = pick('adminCards');
export const sliceUserCards = pick('userCards');
export const sliceShellProviders = pick('shellProviders');
export const sliceHostedProviders = pick('hostedProviders');
export const sliceAppBarActions = pick('appBarActions');
export const sliceBanners = pick('banners');
export const sliceOverlays = pick('overlays');
export const sliceUserMenuItems = pick('userMenuItems');
export const sliceConsolePermissions = pick('consolePermissions');

/** Runs every enabled slice's `setup()` once, before the first render. */
export function setupSlices(): void {
  for (const slice of ENABLED_WEB) slice.setup?.();
}

/** Runs before sign-out: every enabled slice's `beforeLogout`, each allowed to fail. */
export async function beforeLogout(): Promise<void> {
  await Promise.allSettled(ENABLED_WEB.map((slice) => slice.beforeLogout?.()));
}

/** The feature flags the enabled slices expose, as a hook (the set of slices never changes, so the hook order is fixed). */
export function useSliceFeatures(): Record<string, boolean> {
  const features: Record<string, boolean> = {};
  for (const slice of ENABLED_WEB) Object.assign(features, slice.useFeatures?.());
  return features;
}
