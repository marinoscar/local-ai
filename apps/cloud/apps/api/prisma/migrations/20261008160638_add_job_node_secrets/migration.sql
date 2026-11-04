-- =============================================================================
-- Per-job secret broker: the handle ledger (issue #349, epic #345)
-- =============================================================================
-- One table, and the interesting thing about it is a column that is NOT here.
--
-- A worker node holds no credentials by design (docs/specs/worker-nodes.md §8).
-- Epic #345 needs it to run work that genuinely requires one, so the server
-- mints a credential per job, hands it over exactly once, and destroys it when
-- the job settles. `job_node_secrets` records THAT A GRANT EXISTED — which node,
-- of what kind, from when until when, revoked or not — so "did a node ever hold
-- a credential to this database, and when" is answerable.
--
-- ⚠ THERE IS NO COLUMN CAPABLE OF HOLDING SECRET MATERIAL, AND ADDING ONE IS
-- THE ONE CHANGE THIS TABLE MUST NEVER RECEIVE. Not `password`, not
-- `encrypted_material`, not "just a base64 blob for debugging". `handle` is an
-- identifier for the grant (a role name) and is what `JobSecretBroker.revoke`
-- takes; the material is serialised into one HTTP response and dropped.
-- Reusing SECRETS_ENCRYPTION_KEY here was considered and rejected: a credential
-- that can be re-read is a credential that can be stolen twice, and nothing
-- needs to re-read this one. Storing it — even encrypted — would put a live
-- database password into this table, into every pg_dump of it, and into every
-- copy of that dump. See the block comment above `JobNodeSecret` in
-- prisma/schema.prisma for the full argument.
--
-- NO FOREIGN KEYS, deliberately. `job_id` and `node_id` are plain UUIDs. A
-- `jobs` row is deleted on a retention schedule (`job.history.purge`) that has
-- nothing to do with a grant's lifetime, and a cascade from that purge would
-- erase the audit record this table exists to keep; a grant must also stay
-- revocable after its job row is gone, which is why the sweeper resolves a
-- broker by `kind` rather than by the job's `type`.
-- =============================================================================

-- CreateTable
CREATE TABLE "job_node_secrets" (
    "id" UUID NOT NULL,
    "job_id" UUID NOT NULL,
    "node_id" UUID NOT NULL,
    "kind" TEXT NOT NULL,
    -- The grant's identifier. NEVER the password — see the header.
    "handle" TEXT NOT NULL,
    "issued_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expires_at" TIMESTAMPTZ NOT NULL,
    "revoked_at" TIMESTAMPTZ,

    CONSTRAINT "job_node_secrets_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
-- ONE CREDENTIAL PER JOB, EVER. A node asks for its secret more than once as a
-- matter of course (a restarted process still holding the lease, a lost
-- response, a retry), and every one of those calls must extend the SAME grant.
-- The revocation paths know exactly one handle per (job, kind); a sibling grant
-- would be one nothing ever destroys. This index makes the second row
-- unrepresentable rather than leaving it to the service to remember.
CREATE UNIQUE INDEX "job_node_secrets_job_id_kind_key" ON "job_node_secrets"("job_id", "kind");

-- CreateIndex
-- The sweeper's scan: unrevoked grants whose own clock has lapsed. See
-- `NodeSecretBrokerService.sweep` for why the sweeper is not redundant with the
-- settle-event revoker — three real cases emit no event at all.
CREATE INDEX "job_node_secrets_expires_at_idx" ON "job_node_secrets"("expires_at");
