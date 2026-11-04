// What the cloud's Doctor checks share: the category, the thresholds of the spec (docs/specs/cloud-app.md section 7)
// and the injection tokens of the two ports a unit test replaces.

/** The category of every check below (the report sorts an unknown category after the platform's, in registration order). */
export const LOCALAI_DOCTOR_CATEGORY = 'localai';

/** A node added in the wizard and still `pending` after this long is reported (LAI-CLD-031). */
export const PENDING_NODE_AFTER_MS = 7 * 24 * 60 * 60 * 1000;

/** A queued command past its expiry, or a running one past its lease, is reported when this old (LAI-CLD-030). */
export const COMMAND_SWEEP_GRACE_MS = 5 * 60 * 1000;

/** An enrolled node with no stored key is reported once it has been enrolled this long (LAI-CLD-026). */
export const NODE_KEY_GRACE_MS = 10 * 60 * 1000;

/** `aictl version --json` must answer within this (the same bound as the check's own ceiling). */
export const AICTL_PROBE_TIMEOUT_MS = 5_000;

/** The platform's `nodes` policy is reported when a node record would be pruned sooner than this many days... */
export const MIN_OFFLINE_RETENTION_DAYS = 365;

/** ...or when a node would count as stale only after more than this many seconds. */
export const MAX_STALE_HEARTBEAT_SECONDS = 90;

/** The cipher round trip (no stored key to read yet). Replaced by a fake in unit tests. */
export const LOCALAI_DOCTOR_CIPHER = Symbol('LOCALAI_DOCTOR_CIPHER');

/** The `aictl version --json` runner. Replaced by a fake in unit tests. */
export const LOCALAI_DOCTOR_AICTL = Symbol('LOCALAI_DOCTOR_AICTL');
