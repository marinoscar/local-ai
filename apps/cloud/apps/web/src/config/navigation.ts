import AdminPanelSettingsIcon from '@mui/icons-material/AdminPanelSettings';
import HomeIcon from '@mui/icons-material/Home';
import HubIcon from '@mui/icons-material/Hub';
import KeyIcon from '@mui/icons-material/VpnKey';
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
export const NAVIGATION: ShellNavigation<'home' | 'lab' | 'gateway' | 'settings' | 'console'> = {
  destinations: [
    { key: 'home', label: 'Home', compactLabel: 'Home', Icon: HomeIcon, path: '/' },
    { key: 'lab', label: 'Lab', compactLabel: 'Lab', Icon: HubIcon, path: '/lab', permission: 'lab:read' },
    // API keys of the gateway (docs/specs/gateway.md section 3.8): gateway:use is the permission GET /api/localai/gateway/keys enforces.
    // The Usage and Models pages of the gateway (GW-08, GW-09) belong to this destination too.
    { key: 'gateway', label: 'API keys', compactLabel: 'Keys', Icon: KeyIcon, path: '/gateway/keys', permission: 'gateway:use' },
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
  destinationRoutes: { home: ['/'], lab: ['/lab'], gateway: ['/gateway'], settings: ['/settings'], console: ['/admin'] },
  settingsSurfaces: [
    { sections: ADMIN_SECTIONS, hubPath: '/admin/settings', hubTitle: 'Administration' },
    { sections: USER_SETTINGS_SECTIONS, hubPath: '/settings', hubTitle: 'Settings' },
  ],
  console: { prefix: '/admin', sections: ADMIN_SECTIONS },
};
