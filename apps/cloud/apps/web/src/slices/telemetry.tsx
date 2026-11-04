// The telemetry slice, on the web: the Telemetry settings page (where collection
// is switched on and the GreptimeDB connection saved), the SQL Explorer with its
// AI assistant, and the Dashboard with the metric groups the API reports (the
// app's own `activity` group needs no web code). The three pages share the
// `Observability` group of the Console.
//
// The settings page is NOT behind `RequireTelemetryEnabled`: it is where
// telemetry is switched on. The Explorer and the Dashboard are, and their cards
// declare `feature: 'telemetry'`, so the hubs hide them while collection is off.
//
// The assistant needs the AI slice; without it the Explorer works and the
// assistant stays hidden (`useAiEnabled` answers `false`).
import { useAiConfig, useAiModels, type AiModel, type AiModelListFilter } from '@marinoscar/platform-web/ai/headless';
import { registerSettingsFeature } from '@marinoscar/platform-web/settings/headless';
import {
  RequireTelemetryEnabled,
  TelemetryConfigProvider,
  TelemetryWebAdaptersProvider,
  useTelemetryFeatures,
  type TelemetryAiEnabledState,
  type TelemetryAssistantModelOption,
  type TelemetryAssistantModelsState,
  type TelemetryWebAdapters,
} from '@marinoscar/platform-web/telemetry/headless';
import { telemetryAdminCards } from '@marinoscar/platform-web/telemetry/ui';
import { lazy, useMemo, type ReactNode } from 'react';
import { ENABLED_SLICES } from '@app/shared';

import { platformApi } from '../api';
import type { WebSlice } from './slice';

const TelemetrySettingsPage = lazy(() => import('@marinoscar/platform-web/telemetry/ui/settings-page'));
const TelemetryExplorerPage = lazy(() => import('@marinoscar/platform-web/telemetry/ui/explorer-page'));
const TelemetryDashboardPage = lazy(() => import('@marinoscar/platform-web/telemetry/ui/dashboard-page'));

// The `telemetry` feature, for the settings cards that declare it.
declare module '@marinoscar/platform-web/settings/headless' {
  interface SettingsFeatureRegistry {
    telemetry: true;
  }
}

/** One `GET /api/telemetry/config` for the chrome and every page. Above the platform host, which reads the feature it answers. */
function TelemetryConfig({ children }: { children: ReactNode }) {
  return <TelemetryConfigProvider api={platformApi}>{children}</TelemetryConfigProvider>;
}

/** Enabled models only; one page of 100 is the whole catalogue in practice. */
const ASSISTANT_MODEL_FILTER: AiModelListFilter = Object.freeze({ enabled: true, pageSize: 100 });

function toAssistantModelOption(model: AiModel): TelemetryAssistantModelOption {
  return {
    id: model.id,
    provider: model.provider,
    modelId: model.modelId,
    label: model.displayName || model.modelId,
    supportsToolCalling: model.capabilities?.capabilities.includes('tools') ?? false,
  };
}

// The AI hooks read the AI config provider, which only the AI slice mounts, so
// they are used only while that slice is on. The choice is made once, at load:
// the set of slices never changes at run time, so the hook order is fixed.
const AI_ON = ENABLED_SLICES.includes('ai');

function useAiEnabled(): TelemetryAiEnabledState {
  const { config, isLoading } = useAiConfig();
  return { enabled: config.enabled, isLoading };
}

function useAssistantModels(): TelemetryAssistantModelsState {
  const { models, isLoading, error } = useAiModels(ASSISTANT_MODEL_FILTER);
  const options = useMemo(() => models.map(toAssistantModelOption), [models]);
  return { models: options, isLoading, error };
}

const NO_AI_ENABLED = (): TelemetryAiEnabledState => ({ enabled: false, isLoading: false });
const NO_ASSISTANT_MODELS = (): TelemetryAssistantModelsState => ({ models: [], isLoading: false, error: null });

/** A module constant: each `use*` member is a hook, so the same object must be handed in on every render. */
const TELEMETRY_ADAPTERS: TelemetryWebAdapters = Object.freeze({
  useAiEnabled: AI_ON ? useAiEnabled : NO_AI_ENABLED,
  useAssistantModels: AI_ON ? useAssistantModels : NO_ASSISTANT_MODELS,
});

function TelemetryAdapters({ children }: { children: ReactNode }) {
  return <TelemetryWebAdaptersProvider adapters={TELEMETRY_ADAPTERS}>{children}</TelemetryWebAdaptersProvider>;
}

export const telemetryWebSlice: WebSlice = {
  id: 'telemetry',
  setup: () => registerSettingsFeature('telemetry', () => useTelemetryFeatures().telemetry),
  shellProviders: [TelemetryConfig, TelemetryAdapters],
  useFeatures: () => ({ telemetry: useTelemetryFeatures().telemetry }),
  routes: [
    { path: 'admin/settings/telemetry', permission: 'telemetry:read', element: <TelemetrySettingsPage /> },
    {
      path: 'admin/settings/telemetry/explorer',
      permission: 'telemetry:query',
      element: (
        <RequireTelemetryEnabled>
          <TelemetryExplorerPage />
        </RequireTelemetryEnabled>
      ),
    },
    {
      path: 'admin/settings/telemetry/dashboard',
      permission: 'telemetry:query',
      element: (
        <RequireTelemetryEnabled>
          <TelemetryDashboardPage />
        </RequireTelemetryEnabled>
      ),
    },
  ],
  adminCards: [{ group: 'Observability', cards: telemetryAdminCards }],
  consolePermissions: ['telemetry:read'],
};
