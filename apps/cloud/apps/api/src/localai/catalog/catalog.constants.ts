// Constants and shared shapes of the catalog slice (docs/specs/cloud-app.md section 4.6, catalog.md section 1).
//
// Everything here is a permanent contract: the path rule, the limits and the job type strings are stored in rows,
// jobs and audit events, and the node-facing body is parsed by the agent (`agent/internal/catalog/fetch.go`).

/** The only paths a catalog may carry (the agent's `bundlePathRe`, the same regex). */
export const CATALOG_PATH_RE = /^(CATALOG_VERSION|models\.yaml|ark\.yaml|profiles\/[a-z0-9-]+\.yaml)$/;

/** The most a catalog may weigh in total (the agent refuses a bundle over this, catalog.md section 1). */
export const MAX_CATALOG_BYTES = 2 * 1024 * 1024;

/** The most a downloaded release archive may weigh (compressed). */
export const MAX_ARCHIVE_BYTES = 20 * 1024 * 1024;

/**
 * The most a release archive may inflate to. The archive is the whole repository at that commit (sources, docs,
 * vendored dependencies), so this is far above the catalog's 2 MB and only guards against a decompression bomb.
 */
export const MAX_ARCHIVE_UNCOMPRESSED_BYTES = 512 * 1024 * 1024;

/** The most entries a release archive may list. */
export const MAX_ARCHIVE_ENTRIES = 200_000;

/** The download's overall time limit. */
export const ARCHIVE_FETCH_TIMEOUT_MS = 30_000;

/** `aictl catalog lint` is killed after this long. */
export const LINT_TIMEOUT_MS = 30_000;

/** The most `aictl` may print before the run is judged broken (a lint report is a few KB). */
export const LINT_MAX_OUTPUT_BYTES = 1024 * 1024;

/** The default source of `catalog-*` tags (the `catalog.githubRepo` setting's default, cloud-app.md section 3.10). */
export const DEFAULT_CATALOG_GITHUB_REPO = 'marinoscar/local-ai';

/** The job types (permanent strings once jobs of the type exist). */
export const JOB_CATALOG_IMPORT = 'localai.catalog.import';
export const JOB_CATALOG_LINT = 'localai.catalog.lint';

/** `subjectType` of both jobs; `subjectId` is the catalog version, so one version has at most one active job per type. */
export const CATALOG_JOB_SUBJECT = 'localai_catalog';

/** Audit actions (cloud-app.md section 3.5). `targetType` is `localai_catalog`, `targetId` the version. */
export const AUDIT_CATALOG_IMPORTED = 'localai.catalog.imported';
export const AUDIT_CATALOG_LINTED = 'localai.catalog.linted';
export const AUDIT_CATALOG_APPROVED = 'localai.catalog.approved';
export const AUDIT_CATALOG_TARGET = 'localai_catalog';

/** The doctor codes this slice answers (docs/specs/doctor-codes.md, area CLD). */
export const LAI_CLD_CATALOG_UNAVAILABLE = 'LAI-CLD-013';
export const LAI_CLD_AICTL_MISSING = 'LAI-CLD-033';

/** The command a notified node runs (cloud-app.md section 3.7) and its dedup key (one active per node). */
export const COMMAND_CATALOG_REFRESH = 'localai.catalog.refresh';
export const COMMAND_CATALOG_REFRESH_DEDUP = 'localai.catalog.refresh:-';
/** How long a queued `catalog.refresh` waits for a node to claim it (cloud-app.md section 3.7 table: 1 h). */
export const COMMAND_CATALOG_REFRESH_TTL_MS = 60 * 60 * 1000;

/** One file of a stored catalog: the unit of the `files` JSON column and of the node-facing body. */
export interface CatalogFile {
  path: string;
  sha256: string;
  content: string;
}

/** One problem `aictl catalog lint --json` reports (`agent/internal/catalog` `Problem`). */
export interface LintProblem {
  code: string;
  file: string;
  path: string;
  message: string;
}

/** Why a lint run did not produce a verdict. `LAI-CLD-033` is the one doctor code for every such reason. */
export type LintFailureReason = 'aictl_missing' | 'aictl_failed' | 'timeout' | 'bad_output';

/** What `lint_result` holds: a verdict with its problems, or an error and no verdict. */
export interface LintResult {
  valid?: boolean;
  problems?: LintProblem[];
  error?: { code: typeof LAI_CLD_AICTL_MISSING; reason: LintFailureReason; message: string };
  /** The exit status `aictl` ended with (absent when it could not be started or was killed). */
  exitCode?: number;
}
