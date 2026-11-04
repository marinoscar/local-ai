// An in-memory `KeyStore` for the unit tests of key management (the real queries are `test/localai/gateway-keys.db.spec.ts`).
import { randomUUID } from 'node:crypto';

import type { KeyChanges, KeyListFilter, KeyRecord, KeyStore, NewKey } from './key.store';

export class FakeKeyStore implements KeyStore {
  readonly rows: KeyRecord[] = [];
  readonly users = new Set<string>();
  private tick = 0;

  insert(data: NewKey): Promise<KeyRecord> {
    const row: KeyRecord = {
      id: randomUUID(),
      ...data,
      revokedAt: null,
      lastUsedAt: null,
      createdAt: new Date(Date.UTC(2026, 10, 4, 12, 0, this.tick++)),
      user: { email: `owner-${data.userId.slice(0, 4)}@example.test`, displayName: null },
    };
    this.rows.push(row);
    return Promise.resolve({ ...row });
  }

  findById(id: string): Promise<KeyRecord | null> {
    const row = this.rows.find((candidate) => candidate.id === id);
    return Promise.resolve(row ? { ...row } : null);
  }

  list(filter: KeyListFilter): Promise<KeyRecord[]> {
    return Promise.resolve(
      this.rows
        .filter((row) => (filter.ownerUserId === undefined || row.userId === filter.ownerUserId) && (filter.includeRevoked || row.revokedAt === null))
        .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime())
        .map((row) => ({ ...row })),
    );
  }

  update(id: string, changes: KeyChanges): Promise<KeyRecord | null> {
    const row = this.rows.find((candidate) => candidate.id === id && candidate.revokedAt === null);
    if (!row) return Promise.resolve(null);
    Object.assign(row, changes);
    return Promise.resolve({ ...row });
  }

  revoke(id: string, at: Date): Promise<boolean> {
    const row = this.rows.find((candidate) => candidate.id === id && candidate.revokedAt === null);
    if (!row) return Promise.resolve(false);
    row.revokedAt = at;
    return Promise.resolve(true);
  }

  userExists(userId: string): Promise<boolean> {
    return Promise.resolve(this.users.has(userId));
  }
}
