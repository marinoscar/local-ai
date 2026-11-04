// What the enabled slices contribute to each other's lists, merged once in
// mount order. Pure data (each slice's `contribute()` is evaluated here and
// nowhere else), so any registration step can read it.
import { ENABLED } from './manifest';
import type { SliceContributions } from './slice';

export type MergedContributions = Required<{ [K in keyof SliceContributions]: NonNullable<SliceContributions[K]> }>;

let merged: MergedContributions | undefined;

/** What every enabled slice contributes, merged in mount order (evaluated once). */
export function contributions(): MergedContributions {
  if (merged === undefined) {
    const all = ENABLED.map((slice) => slice.contribute?.() ?? {});
    merged = {
      notifications: all.flatMap((c) => c.notifications ?? []),
      emailTemplates: all.flatMap((c) => c.emailTemplates ?? []),
      storagePrefixes: all.flatMap((c) => c.storagePrefixes ?? []),
      credentialPurposes: all.flatMap((c) => c.credentialPurposes ?? []),
      systemSettings: all.flatMap((c) => c.systemSettings ?? []),
      userSettings: all.flatMap((c) => c.userSettings ?? []),
    };
  }
  return merged;
}
