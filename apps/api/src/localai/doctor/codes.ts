// The cloud's doctor codes as one map (docs/specs/cloud-app.md section 8, docs/specs/doctor-codes.md area CLD).
//
// The Doctor checks (this folder) and the alerts (CLOUD-18) answer with the SAME text for the same code, so the title
// and the fix live here once. The wording is the registry's, word for word: `codes.spec.ts` compares every entry with
// the row of `docs/specs/doctor-codes.md`. A code string is never invented anywhere else.
export type CloudCodeSeverity = 'info' | 'warning' | 'error' | 'critical';

export interface CloudCode {
  readonly code: string;
  readonly severity: CloudCodeSeverity;
  /** The registry's "Meaning" column (plain language, en). */
  readonly title: string;
  /** The registry's "Plain-language fix" column (en). */
  readonly fix: string;
}

export const CLOUD_CODES = {
  /** A node reported no API key to the cloud, so the gateway cannot use it. */
  nodeKeyMissing: {
    code: 'LAI-CLD-026',
    severity: 'warning',
    title: 'The cloud has no API key for this node, so the gateway can\'t use it',
    fix: 'Press "Refresh status" on the node; if it persists run `sudo aictl cloud resend-key` on the node',
  },
  /** Commands wait longer than the sweeper should allow. */
  commandsWaiting: {
    code: 'LAI-CLD-030',
    severity: 'warning',
    title: 'Commands are waiting longer than they should',
    fix: "Admin: check that the cloud's background jobs run (Admin → Jobs)",
  },
  /** A node was added in the wizard and never connected. */
  nodeNeverConnected: {
    code: 'LAI-CLD-031',
    severity: 'warning',
    title: 'A node was added but never connected',
    fix: 'Finish the install on that computer, or retire the node in the cloud',
  },
  /** The credential store cannot decrypt (SECRETS_ENCRYPTION_KEY missing or wrong). */
  credentialsUnreadable: {
    code: 'LAI-CLD-032',
    severity: 'critical',
    title: "The cloud can't read its stored keys (encryption key missing or wrong)",
    fix: 'Admin: restore `SECRETS_ENCRYPTION_KEY` from the backup, restart, then "Ask all nodes to re-send keys"',
  },
  /** The catalog checker is missing or broken on the cloud server. */
  aictlMissing: {
    code: 'LAI-CLD-033',
    severity: 'error',
    title: 'The catalog checker (`aictl`) is missing on the cloud server',
    fix: 'Admin: redeploy the cloud app (`laictl deploy update`)',
  },
  /** Used only as the hint of "no approved catalog": nodes cannot download one. */
  catalogUnavailable: {
    code: 'LAI-CLD-013',
    severity: 'warning',
    title: "Couldn't download the catalog from the cloud",
    fix: 'The node keeps its current catalog. It retries at the next sync',
  },
} as const satisfies Record<string, CloudCode>;

export type CloudCodeKey = keyof typeof CLOUD_CODES;
