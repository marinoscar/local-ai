// The persistence port of key management. `PrismaKeyStore` is the real one; the unit tests use an in-memory fake, so the
// permission rules are tested without a database. Every method is one statement: the service makes no transaction.
import type { LocalaiGatewayKey } from '@prisma/client';

/** A key row with the owner's display data (shown to administrators only). */
export type KeyRecord = LocalaiGatewayKey & { user?: { email: string; displayName: string | null } | null };

/** The columns of a new key. The raw token is not here: only its hash and display prefix. */
export interface NewKey {
  userId: string;
  name: string;
  kind: 'user' | 'service';
  tokenHash: string;
  tokenPrefix: string;
  allowedModels: string[];
  rpmLimit: number;
  concurrentLimit: number;
  dailyTokenBudget: bigint | null;
  expiresAt: Date | null;
  createdById: string;
}

/** The columns an update may change. */
export interface KeyChanges {
  name?: string;
  allowedModels?: string[];
  rpmLimit?: number;
  concurrentLimit?: number;
  dailyTokenBudget?: bigint | null;
}

export interface KeyListFilter {
  /** Only this owner's keys; absent means every owner's (administrators). */
  ownerUserId?: string;
  includeRevoked: boolean;
}

export const GATEWAY_KEY_STORE = Symbol('GATEWAY_KEY_STORE');

export interface KeyStore {
  insert(data: NewKey): Promise<KeyRecord>;
  findById(id: string): Promise<KeyRecord | null>;
  /** Newest first. */
  list(filter: KeyListFilter): Promise<KeyRecord[]>;
  /** Applies `changes` to a key that is not revoked; null when there is no such live key. */
  update(id: string, changes: KeyChanges): Promise<KeyRecord | null>;
  /** Sets `revokedAt` if it is still null. True when this call revoked it. */
  revoke(id: string, at: Date): Promise<boolean>;
  userExists(userId: string): Promise<boolean>;
}
