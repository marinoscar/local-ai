// The email slice, on the web: choose how the application sends email (SES or
// SMTP), and send a test message. Reading needs `system_settings:read` (the
// string `GET /api/email-settings` enforces); saving and testing are gated
// inside the page on `system_settings:write`.
import EmailOutlinedIcon from '@mui/icons-material/EmailOutlined';
import { lazy } from 'react';

import type { WebSlice } from './slice';

const EmailSettingsPage = lazy(() => import('@marinoscar/platform-web/email/ui'));

export const emailWebSlice: WebSlice = {
  id: 'email',
  routes: [{ path: 'admin/settings/email', permission: 'system_settings:read', element: <EmailSettingsPage /> }],
  adminCards: [
    {
      group: 'General',
      cards: [
        {
          title: 'Email',
          description: 'Choose how the application sends email, and send a test message to prove it works.',
          Icon: EmailOutlinedIcon,
          path: '/admin/settings/email',
          permission: 'system_settings:read',
        },
      ],
    },
  ],
};
