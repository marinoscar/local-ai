// =============================================================================
// The optional platform slices, as data
// =============================================================================
//
// Which slices this app mounts is ONE list: `enabled` in
// `packages/shared/slices.json` (the API and the web app both read it). A
// slice is the packaged `forRoot()`, its host-port adapters, its registrations
// and one minimal example, kept in `src/platform/<id>/` and described to the
// rest of the app by an `ApiSlice` (`./definitions.ts` lists them all).
//
// What each slice requires, its label and the order they mount in are the
// catalog in `slices.json` (validated by `@app/shared`, shared with the web app).
//
// A slice definition is DATA PLUS LAZY LOADERS: importing one runs nothing,
// so a disabled slice costs no `forRoot()`, no registry entry and no route.
// Everything that touches a static registry or builds a Nest module lives
// behind a function that is called only for an enabled slice.
// =============================================================================

import type { DynamicModule, Provider, Type } from '@nestjs/common';
import type { PlatformPermissionSlice } from '@marinoscar/platform-api/manifest';

import type { SliceId } from '@app/shared';

export { SLICE_IDS, type SliceId } from '@app/shared';

/** A Nest module a slice mounts. */
export type SliceModule = Type<unknown> | DynamicModule;

/** The enabled slices, for a definition that adapts to its neighbours. */
export interface EnabledSlices {
  has(id: SliceId): boolean;
  readonly ids: readonly SliceId[];
}

/**
 * What a slice adds to lists another slice owns. The owning slice (or the
 * composition) registers the merged lists once, in slice order, so a slice
 * never reaches into another's files.
 */
export interface SliceContributions {
  /** Notification events with their email and browser renderers (the notifications slice registers them). */
  readonly notifications?: readonly import('@marinoscar/platform-api/notifications').NotificationRegistration[];
  /** Email templates this slice owns the words of (the email slice registers them). */
  readonly emailTemplates?: readonly import('@marinoscar/platform-api/email').EmailTemplateEntry[];
  /** Object-storage key prefixes (the storage purge reaches them). */
  readonly storagePrefixes?: readonly import('@marinoscar/platform-api/storage').StorageKeyPrefixDef[];
  /** Purposes the credential stores accept. */
  readonly credentialPurposes?: readonly import('@marinoscar/platform-api/credentials').CredentialPurposeDef[];
  /** System settings namespaces (registered before `SettingsModule.forRoot()` composes the request bodies). */
  readonly systemSettings?: readonly import('@marinoscar/platform-api/settings').SystemSettingsNamespace[];
  /** Optional user settings namespaces. */
  readonly userSettings?: readonly import('@marinoscar/platform-api/settings').UserSettingsNamespace[];
}

/** What a slice adds to the user-data module's options (the data resets). */
export interface SliceUserDataOptions {
  /** Rules that run before a user row is deleted (a last-admin rule). */
  readonly userRemovalHooks?: readonly import('@marinoscar/platform-api/user-data').UserRemovalHook[];
  /** `DatabaseBackupRun.jobId`-style references that keep their jobs through a factory reset. */
  readonly keepJobsReferencedBy?: readonly { readonly model: string; readonly field: string }[];
}

export interface ApiSlice {
  readonly id: SliceId;
  /** The slice's name in `PlatformPermissionOptions.slices`, when it declares permissions (seeded by `prisma:seed`). */
  readonly permissionSlices?: readonly PlatformPermissionSlice[];
  /** The slice's registry contributions. Lazy: evaluated for an enabled slice only. */
  readonly contribute?: () => SliceContributions;
  /**
   * Files of the slice that issue raw SQL, relative to `src/`, each with the reason
   * (the `user-owned-data` conformance suite allowlists them; every other raw
   * statement fails it).
   */
  readonly rawSql?: readonly { readonly file: string; readonly why: string }[];
  /** The slice's contribution to the data resets. Lazy, like `contribute`. */
  readonly userData?: () => SliceUserDataOptions;
  /**
   * Import-time registrations that are the slice's own (an onboarding manifest,
   * a device source, an export source). Runs once, before any `forRoot()`.
   */
  readonly register?: (enabled: EnabledSlices) => void;
  /** The configured Nest modules, built here and nowhere else (`forRoot()` runs when this does). */
  readonly modules?: (enabled: EnabledSlices) => readonly SliceModule[];
  /**
   * Host-port bindings that replace the starter's defaults in the global
   * `AppHostModule` (for example the identity notifier once notifications exist).
   * A provider replaces the default of the same token.
   */
  readonly hostPorts?: (enabled: EnabledSlices) => readonly Provider[];
}
