import { buildDatabaseUrl } from '@marinoscar/platform-api/core';
import { identityConfiguration } from '@marinoscar/platform-api/identity';
import { jobsConfiguration } from '@marinoscar/platform-api/jobs';

/**
 * The app's configuration tree (`ConfigService`). The platform slices read
 * their own keys from it, so each slice's `*Configuration(env)` is spread in
 * as it is; only `nodeEnv`, `port`, `appUrl` and `database` are the app's.
 * Every variable is documented in `infra/compose/.env.example`.
 */
export default () => {
  const databaseUrl = buildDatabaseUrl(process.env);
  // Prisma's driver adapter and the CLI read the same URL.
  process.env.DATABASE_URL = databaseUrl;

  return {
    nodeEnv: process.env.NODE_ENV || 'development',
    port: parseInt(process.env.PORT || '3000', 10),
    appUrl: process.env.APP_URL || 'http://localhost:3535',
    database: { url: databaseUrl },
    ...identityConfiguration(process.env),
    ...jobsConfiguration(process.env),
    // Storage LIMITS (read by the storage slice when it is enabled): how large
    // an upload may be, which media types are allowed (empty allows every
    // type), how long a signed URL lives, how big a multipart part is. There
    // is NO storage configuration here and there must never be one: which
    // bucket, region, endpoint, provider and credential are runtime settings
    // at /admin/settings/storage.
    storage: {
      maxFileSize: parseInt(process.env.MAX_FILE_SIZE || '10737418240', 10),
      allowedMimeTypes: (process.env.ALLOWED_MIME_TYPES ?? '')
        .split(',')
        .map((entry) => entry.trim().toLowerCase())
        .filter((entry) => entry.length > 0),
      signedUrlExpiry: parseInt(process.env.SIGNED_URL_EXPIRY || '3600', 10),
      partSize: parseInt(process.env.STORAGE_PART_SIZE || '10485760', 10),
    },
    // Whether THIS PROCESS runs the database-backup timer (the database-backup
    // slice). The schedule, retention and policy are settings; the literal
    // string `false` turns the timer off, anything else leaves it on.
    dbBackup: { scheduleEnabled: process.env.DB_BACKUP_SCHEDULE_ENABLED !== 'false' },
    // Observability (read by the telemetry slice's Doctor checks and its stack
    // service). `otel.enabled` says whether the SDK was installed at boot
    // (`src/instrumentation.ts`); WHETHER anything is exported is the runtime
    // `telemetry.enabled` setting.
    otel: {
      enabled: process.env.OTEL_ENABLED === 'true',
      endpoint: process.env.OTEL_EXPORTER_OTLP_ENDPOINT,
    },
    // The GreptimeDB read side: the DEPLOYMENT DEFAULT for the connection an
    // administrator can instead save at /admin/settings/telemetry (used wholly
    // in place of this block when saved). The collector's writer login is the
    // collector's and is not read here. Credentials default to '' (never a
    // guessable default).
    greptime: {
      host: process.env.GREPTIME_HOST || '',
      pgPort: parseInt(process.env.GREPTIME_PG_PORT || '4003', 10),
      database: process.env.GREPTIME_DB || 'public',
      readerUser: process.env.GREPTIME_READER_USER || '',
      readerPassword: process.env.GREPTIME_READER_PASSWORD || '',
      adminUser: process.env.GREPTIME_ADMIN_USER || '',
      adminPassword: process.env.GREPTIME_ADMIN_PASSWORD || '',
      available: Boolean(process.env.GREPTIME_HOST && process.env.GREPTIME_READER_USER && process.env.GREPTIME_READER_PASSWORD),
    },
    // The VPS sidecar that holds the Docker socket and starts the telemetry
    // services on request. Deployment infrastructure; either value missing
    // means "not configured", the normal state on a development machine. The
    // token is read only by the stack client and never logged.
    stackAgent: {
      url: (process.env.STACK_AGENT_URL || '').trim(),
      token: (process.env.STACK_AGENT_TOKEN || '').trim(),
    },
  };
};
