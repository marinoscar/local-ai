import { doctorSettingsPage } from '@marinoscar/platform-web/doctor/ui';
import { identityAdminSections } from '@marinoscar/platform-web/identity/ui';
import { jobsAdminSections } from '@marinoscar/platform-web/jobs/ui';
import type { SettingsSectionDef } from '@marinoscar/platform-web/settings/ui';
import { DANGER_ZONE_GROUP_LABEL } from '@marinoscar/platform-web/user-data/headless';
import { factoryResetSettingsPage } from '@marinoscar/platform-web/user-data/ui';

import { composeSections } from '../slices/sections';
import { sliceAdminCards } from '../slices/manifest';

/**
 * The core's admin groups. `General` is empty on purpose: the optional slices
 * (`packages/shared/slices.json`) append their cards to it and to
 * `Operations`, and add their own groups (AI) before the Danger Zone.
 */
const CORE_ADMIN_SECTIONS: SettingsSectionDef[] = [
  { label: 'General', cards: [] },
  { label: 'Access', cards: [...identityAdminSections.access, ...identityAdminSections.organizations] },
  {
    label: 'Operations',
    cards: [
      // The fleet page needs the nodes control plane, which the starter does not mount.
      ...jobsAdminSections.operations.filter((card) => card.path !== '/admin/settings/workers'),
      { ...doctorSettingsPage.card, Icon: doctorSettingsPage.Icon },
    ],
  },
  // `Danger Zone` stays LAST (docs/specs/settings-ui.md): new groups go above it.
  { label: DANGER_ZONE_GROUP_LABEL, cards: [{ ...factoryResetSettingsPage.card, Icon: factoryResetSettingsPage.Icon }] },
];

/**
 * The admin settings hub (`/admin/settings`), as data. Every card's
 * `permission` is the exact string the API route enforces. The packaged
 * slices ship their cards; the app decides where they go. A core card is
 * appended above; an optional slice's card is declared in `src/slices/<id>.tsx`.
 */
export const ADMIN_SECTIONS: SettingsSectionDef[] = composeSections(CORE_ADMIN_SECTIONS, sliceAdminCards);
