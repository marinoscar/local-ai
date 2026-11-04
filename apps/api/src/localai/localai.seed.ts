// Seed step: the worker-node fleet policy a laptop ark needs (docs/specs/cloud-app.md section 3.6, CLOUD-04).
//
// The platform's shipped `nodes` policy prunes a node record after 30 days offline and calls a node stale
// after 90 s. A laptop that is deliberately offline for months must stay in the fleet: pruning would orphan
// `localai_nodes.worker_node_id` (the binding guard heals it on the next contact, but the history is lost).
// So the deployment starts with a 45 s stale window, offline after 4 stale windows (3 min), and a record
// kept for 10 years (3650 days, the schema's maximum).
//
// Standalone and idempotent, like the platform's own steps: no Nest container, no `src/`-only imports
// beyond the Prisma types. It writes only the keys that are ABSENT from the stored `nodes` object, so a value an
// administrator has set on /admin/settings/workers is never overwritten, and a second run changes nothing.
// Stored values win over the platform defaults when settings are read, key by key.
import type { Prisma } from '@prisma/client';
import type { SeedStep } from '@marinoscar/platform-db/seed';

/** The `nodes` system-settings keys this app seeds, with their values (permanent contract of the seed). */
export const LOCALAI_NODES_SETTINGS = {
  staleHeartbeatSeconds: 45,
  offlineStaleMultiplier: 4,
  offlineRetentionDays: 3650,
} as const;

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export const seedLocalaiNodeSettings: SeedStep<Prisma.TransactionClient> = {
  id: 'localai.node-settings',
  async run(tx, ctx) {
    const row = await tx.systemSettings.findUnique({ where: { key: 'global' } });
    if (!row) throw new Error("The 'global' system settings row is missing: the platform seed must run first.");

    const value = isPlainObject(row.value) ? row.value : {};
    const nodes = isPlainObject(value.nodes) ? value.nodes : {};
    const missing = Object.fromEntries(Object.entries(LOCALAI_NODES_SETTINGS).filter(([key]) => nodes[key] === undefined));
    if (Object.keys(missing).length === 0) return { created: 0, updated: 0 };

    // Optimistic concurrency on the row's version, the same contract the settings API uses: a save that
    // landed between the read and this write is never overwritten.
    const written = await tx.systemSettings.updateMany({
      where: { key: 'global', version: row.version },
      data: { value: { ...value, nodes: { ...nodes, ...missing } } as Prisma.InputJsonValue, version: { increment: 1 } },
    });
    if (written.count !== 1) throw new Error("The 'global' system settings row changed while seeding; run the seed again.");
    ctx.log.info(`Seeded nodes settings: ${Object.keys(missing).join(', ')}`);
    return { created: 0, updated: 1 };
  },
};
