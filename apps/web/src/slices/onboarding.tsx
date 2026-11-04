// The onboarding slice, on the web: one `GET /api/onboarding` for the shell, the
// welcome dialog (mounted once), the user's Get started page, the
// administrator's Setup guide and the user menu entry. Feature notices tell a
// user a control needs something an administrator has not set up yet; the
// platform ships three (ai, storage, push), and an app adds its own with
// `registerFeatureNotice({ feature, label, adminPermission, setupHref })` in
// `setup` below.
import { OnboardingProvider } from '@marinoscar/platform-web/onboarding/headless';
import {
  GettingStartedMenuItem,
  WelcomeDialog,
  gettingStartedSettingsPage,
  setupGuideSettingsPage,
} from '@marinoscar/platform-web/onboarding/ui';
import { APP_NAME } from '@app/shared';
import { lazy, type ReactNode } from 'react';

import type { WebSlice } from './slice';

const SetupGuidePage = lazy(() => import('@marinoscar/platform-web/onboarding/ui').then((m) => ({ default: m.SetupGuidePage })));
const GettingStartedPage = lazy(() => import('@marinoscar/platform-web/onboarding/ui').then((m) => ({ default: m.GettingStartedPage })));

function AppOnboardingProvider({ children }: { children: ReactNode }) {
  return <OnboardingProvider appName={APP_NAME}>{children}</OnboardingProvider>;
}

export const onboardingWebSlice: WebSlice = {
  id: 'onboarding',
  // INSIDE the platform host: the onboarding fetch goes through the host's transport.
  hostedProviders: [AppOnboardingProvider],
  overlays: [WelcomeDialog],
  userMenuItems: [(close) => <GettingStartedMenuItem onDone={close} />],
  routes: [
    { path: 'settings/getting-started', element: <GettingStartedPage /> },
    // `system_settings:read` is the permission under which the admin block and the metrics are served.
    { path: 'admin/settings/setup', permission: 'system_settings:read', element: <SetupGuidePage /> },
  ],
  adminCards: [{ group: 'General', cards: [{ ...setupGuideSettingsPage.card, Icon: setupGuideSettingsPage.Icon }] }],
  userCards: [{ group: 'Account', cards: [{ ...gettingStartedSettingsPage.card, Icon: gettingStartedSettingsPage.Icon }] }],
};
