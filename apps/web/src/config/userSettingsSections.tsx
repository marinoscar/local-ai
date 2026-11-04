import { identityUserSettingsSections } from '@marinoscar/platform-web/identity/ui';
import type { SettingsSectionDef } from '@marinoscar/platform-web/settings/ui';
import { DANGER_ZONE_GROUP_LABEL } from '@marinoscar/platform-web/user-data/headless';
import { dangerZoneSettingsPage } from '@marinoscar/platform-web/user-data/ui';

import { sliceUserCards } from '../slices/manifest';
import { composeSections } from '../slices/sections';

/**
 * The core's per-user groups. `Account` is empty on purpose: the optional
 * slices (`packages/shared/slices.json`) append to it (Profile, Notifications,
 * Get started) and to `Security` (AI keys), and add their own groups before
 * the Danger Zone.
 */
const CORE_USER_SECTIONS: SettingsSectionDef[] = [
  { label: 'Account', cards: [] },
  { label: 'Security', cards: [...identityUserSettingsSections.security] },
  // `Danger Zone` stays LAST (docs/specs/settings-ui.md): add new groups above it.
  { label: DANGER_ZONE_GROUP_LABEL, cards: [{ ...dangerZoneSettingsPage.card, Icon: dangerZoneSettingsPage.Icon }] },
];

/**
 * The per-user settings hub (`/settings`). A card's `permission` is the exact
 * string its API controller enforces (docs/specs/settings-ui.md). APPEND new cards; an optional
 * slice's card is declared in `src/slices/<id>.tsx`.
 */
export const USER_SETTINGS_SECTIONS: SettingsSectionDef[] = composeSections(CORE_USER_SECTIONS, sliceUserCards);
