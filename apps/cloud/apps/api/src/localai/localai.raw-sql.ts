// The files of the Local AI feature that issue raw SQL, each with its reason. The `user-owned-data` conformance suite
// (test/conformance.spec.ts) allowlists exactly these, on top of the optional slices' own (`ApiSlice.rawSql`), and
// fails when an allowlisted file stops using raw SQL. Paths are relative to `src/`.
export const LOCALAI_RAW_SQL: readonly { readonly file: string; readonly why: string }[] = [
  {
    file: 'localai/commands/claim.sql.ts',
    why:
      'The command claim needs FOR UPDATE SKIP LOCKED and a per-node lock, which Prisma cannot express. Every value is a ' +
      'bound parameter; the text has no request-derived part. localai_commands has no row-level security and no user scope: ' +
      'the only user link is the requested_by_id actor column, which the statement never reads.',
  },
];
