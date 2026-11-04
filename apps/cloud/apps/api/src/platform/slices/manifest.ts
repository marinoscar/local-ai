// The resolved slice manifest: which optional slices this process mounts.
//
// `ENABLED_SLICES` (`packages/shared/slices.json`, validated by `@app/shared`) is
// the only input. This file maps it to the definitions and exposes the questions the rest of
// the app asks (is X on? which slices' permissions are seeded?). It imports
// definitions, which are pure data, so importing it has no side effect: the
// seed, the permission registry and the composition all read it.
import { ENABLED_SLICES, type SliceId } from '@app/shared';
import type { PlatformPermissionSlice } from '@marinoscar/platform-api/manifest';

import { ALL_SLICES } from './definitions';
import type { ApiSlice, EnabledSlices } from './slice';

/** The enabled slices, in mount order (`@app/shared` has validated the list). */
export const ENABLED: readonly ApiSlice[] = ENABLED_SLICES.map((id) => ALL_SLICES[id]);

/** The enabled slice ids, in mount order. */
export const ENABLED_IDS: readonly SliceId[] = Object.freeze(ENABLED.map((slice) => slice.id));

const IDS = new Set<SliceId>(ENABLED_IDS);

/** The enabled set, handed to a definition that adapts to its neighbours. */
export const enabledSlices: EnabledSlices = { has: (id) => IDS.has(id), ids: ENABLED_IDS };

/** Is this optional slice mounted? */
export function isSliceEnabled(id: SliceId): boolean {
  return IDS.has(id);
}

/** The permission-declaring platform slices the enabled slices bring (seeded by `prisma:seed`). */
export function enabledPermissionSlices(): readonly PlatformPermissionSlice[] {
  return [...new Set(ENABLED.flatMap((slice) => slice.permissionSlices ?? []))];
}
