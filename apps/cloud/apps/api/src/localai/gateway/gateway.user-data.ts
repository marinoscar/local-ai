// The gateway key as user-owned data (docs/specs/gateway.md section 3.6). A key belongs to the user it was issued to:
// a user purge or account deletion deletes it (`onDelete: Cascade`, purge `delete`), and the user's data export lists
// it WITHOUT the token hash. The registration lives here, beside the gateway, and `localai.ownership.ts` spreads it into
// the app's registry, which `platform/registrations.ts` hands to the platform; the `userOwnedData` conformance suite and
// the Doctor's `user-data.registries` check fail until it is there.
//
// The usage tables are not user-owned in the registry's sense: `localai_gateway_usage.user_id` is a plain column with no
// foreign key (history outlives the user), so they are classified as app-owned in `localai.ownership.ts`.
import type { UserOwnedModelDef } from '@marinoscar/platform-api/core';
import type { LocalaiGatewayKey } from '@prisma/client';

/**
 * The columns of a key that the user-data export carries, in order. Everything else is left out of the file: the hash
 * (a bearer-credential equivalent), the owner id (the file is the owner's) and the issuer id.
 */
export const GATEWAY_KEY_EXPORT_FIELDS = [
  'id',
  'name',
  'kind',
  'tokenPrefix',
  'allowedModels',
  'rpmLimit',
  'concurrentLimit',
  'dailyTokenBudget',
  'expiresAt',
  'revokedAt',
  'lastUsedAt',
  'createdAt',
] as const;

/** The registry's `exportOmit`: every column of `localai_gateway_keys` that is not in `GATEWAY_KEY_EXPORT_FIELDS`. */
export const GATEWAY_KEY_EXPORT_OMIT = ['tokenHash', 'userId', 'createdById'] as const;

export const GATEWAY_USER_OWNED_MODELS: readonly UserOwnedModelDef<'LocalaiGatewayKey'>[] = [
  {
    model: 'LocalaiGatewayKey',
    ownerField: 'userId',
    purge: 'delete',
    export: 'include',
    exportOmit: GATEWAY_KEY_EXPORT_OMIT,
    rationale:
      "A user's own gateway key: it is deleted with the user. The export lists its name, prefix, kind, limits and dates; the token hash, which would let anyone who held it check a guessed token, is never exported.",
  },
];

/** A key row, as far as the export mapper may read it: no `tokenHash` is in this type, so the mapper cannot leak it. */
export type GatewayKeyExportSource = Pick<LocalaiGatewayKey, (typeof GATEWAY_KEY_EXPORT_FIELDS)[number]>;

/** One key as the export file shows it (dates as ISO 8601, the budget as a number or, beyond 2^53, its text). */
export interface GatewayKeyExportRow {
  readonly id: string;
  readonly name: string;
  readonly kind: 'user' | 'service';
  readonly tokenPrefix: string;
  readonly allowedModels: readonly string[];
  readonly rpmLimit: number;
  readonly concurrentLimit: number;
  readonly dailyTokenBudget: number | string | null;
  readonly expiresAt: string | null;
  readonly revokedAt: string | null;
  readonly lastUsedAt: string | null;
  readonly createdAt: string;
}

const iso = (date: Date | null): string | null => (date === null ? null : date.toISOString());

function budget(value: bigint | null): number | string | null {
  if (value === null) return null;
  return value <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(value) : value.toString();
}

/** The export row of a key: exactly `GATEWAY_KEY_EXPORT_FIELDS`, never the token hash. */
export function toGatewayKeyExport(row: GatewayKeyExportSource): GatewayKeyExportRow {
  return {
    id: row.id,
    name: row.name,
    kind: row.kind,
    tokenPrefix: row.tokenPrefix,
    allowedModels: [...row.allowedModels],
    rpmLimit: row.rpmLimit,
    concurrentLimit: row.concurrentLimit,
    dailyTokenBudget: budget(row.dailyTokenBudget),
    expiresAt: iso(row.expiresAt),
    revokedAt: iso(row.revokedAt),
    lastUsedAt: iso(row.lastUsedAt),
    createdAt: row.createdAt.toISOString(),
  };
}
