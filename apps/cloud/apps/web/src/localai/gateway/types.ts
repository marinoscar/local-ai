// The wire shapes of the key routes (docs/specs/gateway.md section 3.8), as `apps/api/src/localai/gateway/keys/keys.schemas.ts`
// returns them. There is no token hash anywhere in them, and the raw token is only in `CreatedKey`.

export type KeyKind = 'user' | 'service';
export type KeyStatus = 'active' | 'expired' | 'revoked';

export interface GatewayKey {
  id: string;
  name: string;
  kind: KeyKind;
  /** `lai_` plus 6 characters. */
  tokenPrefix: string;
  /** Empty = every enabled model. */
  allowedModels: string[];
  limits: { rpm: number; concurrent: number; dailyTokens: number | null };
  expiresAt: string | null;
  revokedAt: string | null;
  lastUsedAt: string | null;
  createdAt: string;
  status: KeyStatus;
  ownerUserId: string;
  /** Present for a gateway administrator only. */
  owner?: { email: string; displayName: string | null };
}

export interface CreatedKey {
  key: GatewayKey;
  /** Shown once. Never keep it past the dialog that shows it. */
  token: string;
}

export interface CreateKeyInput {
  name: string;
  allowedModels?: string[];
  /** `null` = never (service keys only). */
  expiresInDays?: number | null;
  kind?: KeyKind;
}

export interface KeyFilters {
  /** gateway:admin: every user's keys. */
  all: boolean;
  includeRevoked: boolean;
}

/** What the create dialog needs from `GET /api/localai/settings`. */
export interface KeyDefaults {
  expiresInDays: number;
  maxUserKeyDays: number;
}
