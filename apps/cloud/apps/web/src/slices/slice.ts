// =============================================================================
// The optional platform slices, on the web side
// =============================================================================
//
// Which slices this app mounts is ONE list: `enabled` in
// `packages/shared/slices.json`, the same file the API reads. A web slice is
// the pages a packaged slice ships bound to this app: its routes, its settings
// cards, the providers and shell parts it needs, and what it sets up at start.
// It is DATA: importing a definition renders and registers nothing (pages are
// lazy), and the composition below only looks at the enabled ones, so a slice
// removed from `slices.json` leaves no route, card, provider or request.
// =============================================================================

import type { ComponentType, ReactElement, ReactNode } from 'react';
import type { SliceId } from '@app/shared';
import type { SettingsCardDef } from '@marinoscar/platform-web/settings/ui';
import type { ShellProvider } from '@marinoscar/platform-web/shell/headless';

export type { SliceId };

/** A route of a slice. `permission` is the exact string(s) its API route enforces. */
export interface SliceRoute {
  /** Relative to the shell, no leading slash: `settings/groups`. */
  readonly path: string;
  readonly element: ReactElement;
  /**
   * Behind `RequirePermission`; redirects home without it. A list admits a holder
   * of ANY of them (the broadcasts page: the system or the organization string).
   * Omit for a route any signed-in user may open.
   */
  readonly permission?: string | readonly string[];
}

/** Cards a slice appends to one group of a settings hub (a new group goes before the Danger Zone). */
export interface SliceCards {
  readonly group: string;
  readonly cards: readonly SettingsCardDef[];
}

export interface WebSlice {
  readonly id: SliceId;
  /** Routes inside the signed-in shell. */
  readonly routes?: readonly SliceRoute[];
  /** Routes OUTSIDE the sign-in gate (a slice's public link page). */
  readonly publicRoutes?: readonly SliceRoute[];
  /** Cards for the Console (`/admin/settings`). APPEND, never insert. */
  readonly adminCards?: readonly SliceCards[];
  /** Cards for the user's settings hub (`/settings`). */
  readonly userCards?: readonly SliceCards[];
  /**
   * Providers around the signed-in shell, outermost first, ABOVE the platform
   * host (so the host's feature map can read them: the AI config).
   */
  readonly shellProviders?: readonly ShellProvider[];
  /** Providers INSIDE the platform host, which read it (the onboarding fetch uses the host's transport). */
  readonly hostedProviders?: readonly ShellProvider[];
  /** The top bar's action buttons (the notification bell). */
  readonly appBarActions?: readonly ComponentType[];
  /** Shell-wide banners above the page; each renders nothing on an ordinary day. */
  readonly banners?: readonly ComponentType[];
  /** Dialogs mounted once for the shell (the welcome dialog). */
  readonly overlays?: readonly ComponentType[];
  /** Rows of the user menu. */
  readonly userMenuItems?: readonly ((close: () => void) => ReactNode)[];
  /** Console destinations: extra `anyPermission` strings that make the Administration entry show. */
  readonly consolePermissions?: readonly string[];
  /** Feature flags the platform host exposes (`viewer.isFeatureEnabled`). A hook; called on every render, in a fixed order. */
  readonly useFeatures?: () => Readonly<Record<string, boolean>>;
  /** Runs once, before the first render (configure a client, register a feature notice, remember a launch). */
  readonly setup?: () => void;
  /** Runs before the user signs out (drop this device's push subscription). */
  readonly beforeLogout?: () => Promise<void> | void;
}
