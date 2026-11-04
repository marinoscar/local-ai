// Composes a settings registry from its core groups and what the enabled slices
// append. A slice's cards go at the end of the group it names; a group that does
// not exist yet is added before the Danger Zone, which stays LAST
// (docs/specs/settings-ui.md). Nothing is inserted between existing cards.
import type { SettingsSectionDef } from '@marinoscar/platform-web/settings/ui';
import { DANGER_ZONE_GROUP_LABEL } from '@marinoscar/platform-web/user-data/headless';

import type { SliceCards } from './slice';

export function composeSections(core: readonly SettingsSectionDef[], contributions: readonly SliceCards[]): SettingsSectionDef[] {
  const sections: SettingsSectionDef[] = core.map((section) => ({ label: section.label, cards: [...section.cards] }));
  for (const { group, cards } of contributions) {
    const existing = sections.find((section) => section.label === group);
    if (existing) {
      existing.cards.push(...cards);
      continue;
    }
    const dangerAt = sections.findIndex((section) => section.label === DANGER_ZONE_GROUP_LABEL);
    const created: SettingsSectionDef = { label: group, cards: [...cards] };
    if (dangerAt === -1) sections.push(created);
    else sections.splice(dangerAt, 0, created);
  }
  return sections;
}
