-- =============================================================================
-- App metadata on users and organizations; composite-key targets (PP-14.27, #945)
-- =============================================================================
--
-- users.metadata and organizations.metadata are JSONB documents an app fills
-- with namespaced fields of its own (registerUserMetadataSchema,
-- registerOrganizationMetadataSchema): one top-level key per registered
-- namespace, validated against that namespace's zod schema on write. They are
-- not a secret store; a secret-named field is refused at registration.
--
-- NOT NULL DEFAULT '{}': every existing row gets an empty document; no backfill
-- and no rewrite (a constant default is stored in the catalog, PostgreSQL 11+).
--
-- The size CHECK is a backstop, not the contract. The writer enforces 16 KiB of
-- compact JSON per row; the constraint allows 32 KiB of jsonb text, because the
-- text form of a jsonb value carries a space after every colon and comma, so
-- the two measures differ by a few percent. A constraint that matched the
-- writer's limit exactly would reject a document the writer accepted.
--
-- ai_runs and grants get a unique index on (id, org_id): the target of a
-- composite foreign key (grant_id, org_id) from an organization-owned table an
-- app adds, exactly as groups and storage_objects already are. A plain key on
-- id alone would let a row of one organization point at another's, since a
-- foreign-key check bypasses row-level security. No existing key changes.
--
-- ai_runs and grants are organization tables under forced row-level security,
-- so the migration runs with the bypass on for its transaction (set_config with
-- is_local = true: transaction-local, never session-level). users and
-- organizations have no row-level security.
-- =============================================================================

BEGIN;
SELECT set_config('app.rls_bypass', 'on', true);

-- AlterTable
ALTER TABLE "users" ADD COLUMN     "metadata" JSONB NOT NULL DEFAULT '{}';
ALTER TABLE "users" ADD CONSTRAINT "users_metadata_size_check" CHECK (jsonb_typeof("metadata") = 'object' AND octet_length("metadata"::text) <= 32768);

-- AlterTable
ALTER TABLE "organizations" ADD COLUMN     "metadata" JSONB NOT NULL DEFAULT '{}';
ALTER TABLE "organizations" ADD CONSTRAINT "organizations_metadata_size_check" CHECK (jsonb_typeof("metadata") = 'object' AND octet_length("metadata"::text) <= 32768);

-- CreateIndex
CREATE UNIQUE INDEX "ai_runs_id_org_id_key" ON "ai_runs"("id", "org_id");

-- CreateIndex
CREATE UNIQUE INDEX "grants_id_org_id_key" ON "grants"("id", "org_id");

COMMIT;
