// The storage slice, on the web: the admin storage page (provider, bucket,
// credential, connection test) and the profile page, where a user uploads a
// picture. The upload itself is `createStorageObjectsClient` of
// `@marinoscar/platform-web/storage/headless` over the app's transport.
import CloudOutlinedIcon from '@mui/icons-material/CloudOutlined';
import PersonIcon from '@mui/icons-material/Person';
import { lazy } from 'react';

import type { WebSlice } from './slice';

const StorageConfigPage = lazy(() => import('@marinoscar/platform-web/storage/ui'));
const UserProfilePage = lazy(() => import('@marinoscar/platform-web/settings/ui').then((m) => ({ default: m.UserProfilePage })));

export const storageWebSlice: WebSlice = {
  id: 'storage',
  routes: [
    // `storage_config:read` is the string `GET /api/admin/storage-config` enforces (NOT `storage:*`, which every user holds).
    { path: 'admin/settings/storage', permission: 'storage_config:read', element: <StorageConfigPage /> },
    { path: 'settings/profile', element: <UserProfilePage /> },
  ],
  adminCards: [
    {
      group: 'General',
      cards: [
        {
          title: 'Storage',
          description: 'Point this deployment at an object store, prove the credentials work, and create the bucket if it is not there yet.',
          Icon: CloudOutlinedIcon,
          path: '/admin/settings/storage',
          permission: 'storage_config:read',
        },
      ],
    },
  ],
  userCards: [
    {
      group: 'Account',
      cards: [
        {
          title: 'Profile',
          description: 'Your display name, profile image and time zone, and the email you signed in with.',
          Icon: PersonIcon,
          path: '/settings/profile',
        },
      ],
    },
  ],
  consolePermissions: ['storage_config:read'],
};
