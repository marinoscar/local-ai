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
  {
    file: 'localai/wizard/checklist.service.ts',
    why:
      'Saving one checklist step merges into the node row\'s publish_checklist JSON, so two steps saved at once must not ' +
      'lose one: the merge runs under SELECT ... FOR UPDATE on that row, which Prisma cannot express. The only value is the ' +
      'row id (a uuid read from the node row, bound as a parameter and cast to uuid), never request text. localai_nodes ' +
      'has no row-level security and no user scope.',
  },
  {
    file: 'localai/vps/vps-state.service.ts',
    why:
      'Storing a finished publish/unpublish run merges the journal and the checklist steps into the node row\'s ' +
      'publish_checklist JSON under SELECT ... FOR UPDATE, so a checklist action made at the same time is not lost (the same ' +
      'merge as the wizard\'s checklist service); Prisma cannot express the row lock. The only value is the row id (a uuid read ' +
      'from the node row, bound as a parameter and cast to uuid), never request text. localai_nodes has no row-level security ' +
      'and no user scope.',
  },
];
