// The AI slice, on the web: the administrator's Config, Models and Usage pages,
// the organization's own keys page, and the user's own keys page. AI is OFF
// until an administrator switches it on at /admin/settings/ai; every AI card
// except that one declares `feature: 'ai'`, so the hubs hide them while it is
// off, and `RequireAiEnabled` redirects a typed URL.
//
// The browser NEVER calls a provider: every call is the API's. A feature page
// uses the slice's hooks (`useAiChat`, `useAiRun`...) against `/api/ai/*`.
import AutoAwesomeOutlinedIcon from '@mui/icons-material/AutoAwesomeOutlined';
import DataUsageOutlinedIcon from '@mui/icons-material/DataUsageOutlined';
import KeyOutlinedIcon from '@mui/icons-material/KeyOutlined';
import ModelTrainingOutlinedIcon from '@mui/icons-material/ModelTrainingOutlined';
import VpnKeyOutlinedIcon from '@mui/icons-material/VpnKeyOutlined';
import { Box, CircularProgress } from '@mui/material';
import { AiConfigProvider, useAiConfig, useAiFeatures } from '@marinoscar/platform-web/ai/headless';
import { registerSettingsFeature } from '@marinoscar/platform-web/settings/headless';
import { lazy, type ReactNode } from 'react';
import { Navigate } from 'react-router-dom';

import { platformApi } from '../api';
import type { WebSlice } from './slice';

const AiConfigPage = lazy(() => import('@marinoscar/platform-web/ai/ui/config-page'));
const AiModelsPage = lazy(() => import('@marinoscar/platform-web/ai/ui/models-page'));
const AiUsagePage = lazy(() => import('@marinoscar/platform-web/ai/ui/usage-page'));
const OrgAiKeysPage = lazy(() => import('@marinoscar/platform-web/ai/ui'));
const UserAiKeysPage = lazy(() => import('@marinoscar/platform-web/ai/ui/keys-page'));

// The `ai` feature, for the settings cards that declare it.
declare module '@marinoscar/platform-web/settings/headless' {
  interface SettingsFeatureRegistry {
    ai: true;
  }
}

/** One `GET /api/ai/config` for the chrome and every page. */
function AiConfig({ children }: { children: ReactNode }) {
  return <AiConfigProvider api={platformApi}>{children}</AiConfigProvider>;
}

/** Sends a typed URL home while AI is off. The AI settings page is deliberately NOT behind it: it is where AI is switched on. */
function RequireAiEnabled({ children }: { children: ReactNode }) {
  const { config, isLoading } = useAiConfig();
  if (isLoading) {
    return (
      <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}>
        <CircularProgress aria-label="Loading" />
      </Box>
    );
  }
  return config.enabled ? <>{children}</> : <Navigate to="/" replace />;
}

export const aiWebSlice: WebSlice = {
  id: 'ai',
  setup: () => registerSettingsFeature('ai', () => useAiFeatures().ai),
  shellProviders: [AiConfig],
  useFeatures: () => ({ ai: useAiFeatures().ai }),
  routes: [
    { path: 'admin/settings/ai', permission: 'ai_config:read', element: <AiConfigPage /> },
    { path: 'admin/settings/ai/models', permission: 'ai_config:read', element: <RequireAiEnabled><AiModelsPage /></RequireAiEnabled> },
    { path: 'admin/settings/ai/usage', permission: 'ai_config:read', element: <RequireAiEnabled><AiUsagePage /></RequireAiEnabled> },
    { path: 'admin/settings/ai/organization-keys', permission: 'org_ai_config:read', element: <RequireAiEnabled><OrgAiKeysPage /></RequireAiEnabled> },
    { path: 'settings/ai', permission: 'ai:use', element: <RequireAiEnabled><UserAiKeysPage /></RequireAiEnabled> },
  ],
  adminCards: [
    {
      group: 'AI',
      cards: [
        {
          title: 'AI',
          description: 'Switch AI on for this deployment, choose whose keys pay for calls, and configure each provider.',
          Icon: AutoAwesomeOutlinedIcon,
          path: '/admin/settings/ai',
          permission: 'ai_config:read',
        },
        {
          title: 'AI Models',
          description: 'Review the models each provider offers, classify what they can do, and choose which ones users may call.',
          Icon: ModelTrainingOutlinedIcon,
          path: '/admin/settings/ai/models',
          permission: 'ai_config:read',
          feature: 'ai',
        },
        {
          title: 'AI Usage',
          description: "See who is calling AI, which models they use, and how much of it the organization's key pays for.",
          Icon: DataUsageOutlinedIcon,
          path: '/admin/settings/ai/usage',
          permission: 'ai_config:read',
          feature: 'ai',
        },
        {
          title: 'Organization AI keys',
          description: "Set your organization's own AI provider keys and see its effective AI policy.",
          Icon: VpnKeyOutlinedIcon,
          path: '/admin/settings/ai/organization-keys',
          permission: 'org_ai_config:read',
          feature: 'ai',
        },
      ],
    },
  ],
  userCards: [
    {
      group: 'Security',
      cards: [
        {
          title: 'AI Keys',
          description: 'Add your own API key for each AI provider, check it works, and see which models it can reach.',
          Icon: KeyOutlinedIcon,
          path: '/settings/ai',
          permission: 'ai:use',
          feature: 'ai',
        },
      ],
    },
  ],
};
