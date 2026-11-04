/** The product's display name (`identity.json` `productName`). */
export declare const APP_NAME: string;
/** One-line description (`identity.json` `tagline`). */
export declare const APP_TAGLINE: string;
/** `APP_NAME` slugified: lowercase, hyphenated, never empty. */
export declare const APP_SLUG: string;
/** `owner/name` of the app's GitHub repository. */
export declare const REPO_SLUG: string;
/** `https://github.com/<REPO_SLUG>`. */
export declare const REPO_URL: string;
/** Brand primary colour, `#rrggbb`. */
export declare const THEME_COLOR: string;
/** First-paint background colour, `#rrggbb`. */
export declare const BACKGROUND_COLOR: string;
/** The CLI binary name; seeds its config directory and `<NAME>_` environment prefix. */
export declare const CLI_NAME: string;
/** An optional platform slice the starter can mount (`slices.json` `catalog`). */
export type SliceId =
  | 'credentials'
  | 'storage'
  | 'email'
  | 'notifications'
  | 'ai'
  | 'db-backup'
  | 'onboarding'
  | 'telemetry';
/** Every optional slice the starter can mount, in mount order (dependencies first). */
export declare const SLICE_IDS: readonly SliceId[];
/** What each slice is and requires, in mount order. */
export declare const SLICE_CATALOG: Readonly<Record<SliceId, { readonly label: string; readonly requires: readonly SliceId[] }>>;
/**
 * Validates slice ids against the catalog and returns them in mount order; throws on an unknown id, a
 * duplicate or a missing requirement, naming the slice and the line to add or remove.
 */
export declare function resolveSliceIds(
  ids: readonly string[],
  catalog?: Readonly<Record<string, { readonly requires: readonly string[] }>>,
): readonly SliceId[];
/** The slices this app mounts: `slices.json` `enabled`, validated, in mount order. */
export declare const ENABLED_SLICES: readonly SliceId[];
