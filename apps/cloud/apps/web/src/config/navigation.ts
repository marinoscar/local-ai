import AdminPanelSettingsIcon from '@mui/icons-material/AdminPanelSettings';
import HomeIcon from '@mui/icons-material/Home';
import SettingsIcon from '@mui/icons-material/Settings';
import type { ShellNavigation } from '@marinoscar/platform-web/shell/headless';

import { sliceConsolePermissions } from '../slices/manifest';
import { ADMIN_SECTIONS } from './adminSections';
import { USER_SETTINGS_SECTIONS } from './userSettingsSections';

/**
 * The app's navigation, declared once: the platform shell's rail, bottom bar
 * (four destinations at most) and user menu draw these, the compact AppBar
 * drills into the two settings hubs, and the rail lists the admin cards on
 * `/admin/*` (Console mode). Every `permission` is the exact string the API
 * enforces; the Console shows for anyone who can open one of its cards.
 */
export const NAVIGATION: ShellNavigation<'home' | 'settings' | 'console'> = {
  destinations: [
    { key: 'home', label: 'Home', compactLabel: 'Home', Icon: HomeIcon, path: '/' },
    { key: 'settings', label: 'Settings', compactLabel: 'Settings', Icon: SettingsIcon, path: '/settings' },
    {
      key: 'console',
      label: 'Administration',
      compactLabel: 'Admin',
      Icon: AdminPanelSettingsIcon,
      path: '/admin/settings',
      anyPermission: ['system_settings:read', 'users:read', 'org_members:read', 'organizations:read', 'jobs:read', ...sliceConsolePermissions],
      pinned: true,
    },
  ],
  destinationRoutes: { home: ['/'], settings: ['/settings'], console: ['/admin'] },
  settingsSurfaces: [
    { sections: ADMIN_SECTIONS, hubPath: '/admin/settings', hubTitle: 'Administration' },
    { sections: USER_SETTINGS_SECTIONS, hubPath: '/settings', hubTitle: 'Settings' },
  ],
  console: { prefix: '/admin', sections: ADMIN_SECTIONS },
};
