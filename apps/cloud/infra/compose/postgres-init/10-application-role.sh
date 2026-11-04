#!/bin/sh
# GENERATED from @marinoscar/platform-infra@0.1.0-next.21 (compose) — do not edit; extend through an infra/compose/app.*.compose.yml overlay
# Source of truth: @marinoscar/platform-infra/compose/postgres-init/10-application-role.sh; re-materialise with `npx platform-infra sync`
# =============================================================================
# Creates the ORDINARY role the application connects as (issue #725 PP-6.5)
# =============================================================================
#
# Row-level security is inert for a superuser or a BYPASSRLS role: every policy,
# FORCE included, is skipped. The postgres image makes POSTGRES_USER a
# superuser, so these development and test databases keep that bootstrap login
# as `postgres` (administration only) and create the role the API runs as here:
#
#   NOSUPERUSER NOBYPASSRLS  so the tenant-isolation policies apply to it;
#   CREATEDB                 so restore can create, rename and drop databases;
#   CREATEROLE               so the node-offload broker can mint per-job roles
#                            (with createrole_self_grant set: PostgreSQL 16).
#
# It owns the application database, which makes it the owner of every table the
# migrations create (FORCE ROW LEVEL SECURITY is what applies the policies to an
# owner). Runs once, on the first start of an empty data volume (the image's
# /docker-entrypoint-initdb.d contract). Set APP_DB_USER to "postgres" to opt
# out; the Doctor then reports the role as a superuser and tenant isolation as
# not enforced. Existing volumes are not touched: see
# docs/SECURITY-ARCHITECTURE.md (Tenant isolation (RLS), "The application role").
# =============================================================================
set -eu

if [ -z "${APP_DB_USER:-}" ] || [ "${APP_DB_USER}" = "postgres" ]; then
  echo "application role: APP_DB_USER is unset or 'postgres'; the API will run as a superuser and row-level security will be inert"
  exit 0
fi

psql -v ON_ERROR_STOP=1 --username postgres --dbname "${POSTGRES_DB}" \
  -v app_user="${APP_DB_USER}" -v app_password="${APP_DB_PASSWORD:?APP_DB_PASSWORD is required}" -v app_db="${POSTGRES_DB}" <<'SQL'
CREATE ROLE :"app_user" LOGIN PASSWORD :'app_password' NOSUPERUSER NOBYPASSRLS CREATEDB CREATEROLE;
ALTER DATABASE :"app_db" OWNER TO :"app_user";
ALTER SCHEMA public OWNER TO :"app_user";
-- PostgreSQL 16: a CREATEROLE role that is not a superuser is given ADMIN on a
-- role it creates, but neither INHERIT nor SET, so it could create a job role
-- (the node-offload broker) and then not DROP OWNED BY it or use it as an
-- owner. This makes every role it creates inherit-and-set for it.
ALTER ROLE :"app_user" SET createrole_self_grant = 'inherit, set';
SQL

echo "application role: created ${APP_DB_USER} (NOSUPERUSER NOBYPASSRLS), owner of ${POSTGRES_DB}"
