import { APP_NAME } from '@app/shared';
import { Box, CircularProgress, Typography } from '@mui/material';
import { Fragment, Suspense, type ComponentType } from 'react';
import { Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { DoctorPage } from '@marinoscar/platform-web/doctor/ui';
import {
  AuthProvider,
  IdentityWebAdaptersProvider,
  RequireAuth,
  RequirePermission,
  useAuth,
  useOrgsFeature,
  usePermissions,
} from '@marinoscar/platform-web/identity/headless';
import { AuthCallbackPage, LoginPage, OrganizationPage, OrganizationsPage, UserTokensPage, UsersPage } from '@marinoscar/platform-web/identity/ui';
import { JobInsightsPage, JobsPage } from '@marinoscar/platform-web/jobs/ui';
import { registerSettingsFeature, useSettingsFeatures } from '@marinoscar/platform-web/settings/headless';
import { SettingsHub, type SettingsHubProps } from '@marinoscar/platform-web/settings/ui';
import { FactoryResetPage, OffboardOrganizationButton, UserDangerZonePage, factoryResetSettingsPage } from '@marinoscar/platform-web/user-data/ui';
import { ShellProviders, type ShellProvider } from '@marinoscar/platform-web/shell/headless';
import { ShellAppBar, ShellLayout, ShellUserMenu } from '@marinoscar/platform-web/shell/ui';
import { UserTimeZoneSync } from '@marinoscar/platform-web/timezone/headless';
import type { ReactElement } from 'react';

import { api, platformApi } from './api';
import { ADMIN_SECTIONS } from './config/adminSections';
import { NAVIGATION } from './config/navigation';
import { USER_SETTINGS_SECTIONS } from './config/userSettingsSections';
import { CatalogPage } from './localai/catalog/CatalogPage';
import { LabOverviewPage } from './localai/lab/LabOverviewPage';
import { AddNodeWizard } from './localai/wizard/AddNodeWizard';
import { NodeDetailPage } from './localai/node/NodeDetailPage';
import { AppPlatformHost } from './platformHost';
import {
  beforeLogout,
  sliceAppBarActions,
  sliceBanners,
  sliceHostedProviders,
  slicePublicRoutes,
  sliceOverlays,
  sliceRoutes,
  sliceShellProviders,
  sliceUserMenuItems,
} from './slices/manifest';
import type { SliceRoute } from './slices/slice';

// The `orgs` feature: the organization cards show only in multi-organization mode.
declare module '@marinoscar/platform-web/settings/headless' {
  interface SettingsFeatureRegistry {
    orgs: true;
  }
}
registerSettingsFeature('orgs', useOrgsFeature);

const spinner = (
  <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '60vh' }}>
    <CircularProgress aria-label="Loading" />
  </Box>
);

/** A route behind the permission its card declares (the same string the API enforces). */
function Gate({ permission, children }: { permission: string; children: ReactElement }) {
  return (
    <RequirePermission permission={permission} fallback={<Navigate to="/" replace />}>
      {children}
    </RequirePermission>
  );
}

/**
 * The providers around the signed-in shell, outermost first: the enabled
 * slices' (the notification inbox, the AI config the host's feature map reads),
 * then the platform host every packaged page reads, then the providers that
 * need the host (the onboarding fetch). Add yours to `src/slices/<id>.tsx`.
 */
const SHELL_PROVIDERS: readonly ShellProvider[] = [...sliceShellProviders, AppPlatformHost, ...sliceHostedProviders];

/** Renders each component of a shell slot (banners, overlays, top-bar actions). */
function Slot({ components }: { components: readonly ComponentType[] }) {
  return (
    <>
      {components.map((Component, index) => (
        <Component key={index} />
      ))}
    </>
  );
}

/** The platform shell: AppBar, navigation rail or bottom bar (by width), user menu, and what the enabled slices slot in. */
function AppShell() {
  return (
    <ShellLayout
      navigation={NAVIGATION}
      brand={APP_NAME}
      appBar={
        <ShellAppBar
          navigation={NAVIGATION}
          brand={APP_NAME}
          actions={<Slot components={sliceAppBarActions} />}
          userMenu={
            <ShellUserMenu
              navigation={NAVIGATION}
              items={(close) => sliceUserMenuItems.map((item, index) => <Fragment key={index}>{item(close)}</Fragment>)}
            />
          }
        />
      }
      banners={<Slot components={sliceBanners} />}
      overlays={<Slot components={sliceOverlays} />}
    >
      {/* Slice pages are lazy: the shell stays mounted while one loads. */}
      <Suspense fallback={spinner}>
        <Outlet />
      </Suspense>
    </ShellLayout>
  );
}

const shell = (
  <ShellProviders providers={SHELL_PROVIDERS}>
    <AppShell />
  </ShellProviders>
);

/** A slice's route behind the permission(s) its API route enforces. */
function guarded(route: SliceRoute): ReactElement {
  const { permission, element } = route;
  if (permission === undefined) return element;
  if (typeof permission === 'string') return <Gate permission={permission}>{element}</Gate>;
  return (
    <RequirePermission permissions={[...permission]} fallback={<Navigate to="/" replace />}>
      {element}
    </RequirePermission>
  );
}

function Home() {
  const { user } = useAuth();
  return (
    <Box>
      <Typography variant="h4" component="h1">
        Welcome{user?.displayName ? `, ${user.displayName}` : ''}
      </Typography>
      <Typography color="text.secondary" sx={{ mt: 1 }}>
        Open Settings to get started.
      </Typography>
    </Box>
  );
}

/** The routes. Every settings page is reachable from a registry card (config/). */
export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/auth/callback" element={<AuthCallbackPage />} />
      {/* Slice routes that need no sign-in (a slice's public link page). */}
      {slicePublicRoutes.map((route) => (
        <Route key={route.path} path={`/${route.path}`} element={<Suspense fallback={spinner}>{route.element}</Suspense>} />
      ))}
      <Route element={<RequireAuth loading={spinner} />}>
        <Route element={shell}>
          <Route index element={<Home />} />
          {/* The Local AI lab (docs/specs/cloud-app.md section 3.11). The permission is the string GET /api/localai/nodes enforces. */}
          <Route path="lab" element={<Gate permission="lab:read"><LabOverviewPage /></Gate>} />
          {/* The catalog (section 3.11): reads need lab:read, the writes inside need lab:admin, both enforced by /api/localai/catalog/*. */}
          <Route path="lab/catalog" element={<Gate permission="lab:read"><CatalogPage /></Gate>} />
          {/* The Add-node wizard (section 4.8): the permission is the string POST /api/localai/nodes enforces. */}
          <Route path="lab/nodes/new" element={<Gate permission="lab:admin"><AddNodeWizard /></Gate>} />
          <Route path="lab/nodes/:nodeId" element={<Gate permission="lab:read"><NodeDetailPage /></Gate>} />
          <Route path="settings" element={<Hub sections={USER_SETTINGS_SECTIONS} hubKey="user-settings-hub" title="Settings" subtitle="Manage your account preferences." />} />
          <Route path="settings/tokens" element={<UserTokensPage />} />
          <Route path="admin/settings" element={<Hub sections={ADMIN_SECTIONS} hubKey="admin-settings-hub" title="Administration" subtitle="Users, background jobs and the health of this deployment." />} />
          <Route path="admin/settings/users" element={<Gate permission="users:read"><UsersPage /></Gate>} />
          <Route path="admin/settings/organization" element={<Gate permission="org_members:read"><OrganizationPage /></Gate>} />
          <Route
            path="admin/settings/organizations"
            element={
              <Gate permission="organizations:read">
                <OrganizationsPage
                  renderActions={(organization, { refresh }) => <OffboardOrganizationButton organization={organization} onCompleted={refresh} />}
                />
              </Gate>
            }
          />
          <Route path="admin/settings/jobs" element={<Gate permission="jobs:read"><JobsPage /></Gate>} />
          <Route path="admin/settings/jobs/insights" element={<Gate permission="jobs:read"><JobInsightsPage /></Gate>} />
          <Route path="admin/settings/doctor" element={<Gate permission="system_settings:read"><DoctorPage /></Gate>} />
          <Route path="settings/danger-zone" element={<UserDangerZonePage />} />
          <Route path="admin/settings/factory-reset" element={<Gate permission={factoryResetSettingsPage.card.permission!}><FactoryResetPage /></Gate>} />
          {/* The enabled optional slices' pages (packages/shared/slices.json; src/slices/). */}
          {sliceRoutes.map((route) => (
            <Route key={route.path} path={route.path} element={guarded(route)} />
          ))}
        </Route>
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

/** Both hubs are one packaged component bound to a registry (the Settings UI Pattern). */
function Hub(props: Omit<SettingsHubProps, 'hasPermission' | 'features'>) {
  const { hasPermission } = usePermissions();
  return <SettingsHub {...props} features={useSettingsFeatures()} hasPermission={hasPermission} />;
}

export default function App() {
  return (
    <AuthProvider client={api} onBeforeLogout={beforeLogout}>
      {/* Every packaged time renders in the signed-in user's profile time zone (cleared on sign-out). */}
      <UserTimeZoneSync api={platformApi} />
      <IdentityWebAdaptersProvider adapters={{ appName: APP_NAME }}>
        <AppRoutes />
      </IdentityWebAdaptersProvider>
    </AuthProvider>
  );
}
