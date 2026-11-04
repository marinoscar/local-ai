// Running the real linter: `aictl catalog lint --json <dir>` (docs/specs/cloud-app.md section 4.6, aictl.md 4.11.1).
//
// The cloud does not re-implement the catalog rules: the agent's validator is the single source of them, shipped as
// the `aictl` binary in the API image (`LOCALAI_AICTL_PATH`). This module only starts it safely and maps the result:
//   exit 0 and `valid: true`   -> status `valid`
//   exit 2 and `valid: false`  -> status `invalid`, the problem list stored
//   anything else              -> status stays `draft` with `lintResult.error` and doctor code LAI-CLD-033
//                                 (binary missing, killed by the timeout, crashed, unreadable output)
//
// Safety: an argv array and no shell, the catalog written to a fresh private temp directory that is always removed,
// a timeout, an output ceiling, and an empty environment (the child needs nothing from the API's secrets).
import { execFile } from 'node:child_process';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import { dirname, resolve, sep } from 'node:path';
import { jobTempPath } from '@marinoscar/platform-api/jobs';
import { z } from 'zod';

import {
  CATALOG_PATH_RE,
  LAI_CLD_AICTL_MISSING,
  LINT_MAX_OUTPUT_BYTES,
  LINT_TIMEOUT_MS,
  type CatalogFile,
  type LintFailureReason,
  type LintResult,
} from './catalog.constants';

/** The port the lint job runs the linter through (tests substitute a fake; the default runs the real binary). */
export const CATALOG_LINTER = Symbol('LOCALAI_CATALOG_LINTER');

export type LintOutcome = { status: 'valid' | 'invalid' | 'draft'; result: LintResult };

export interface CatalogLinter {
  lint(files: readonly CatalogFile[]): Promise<LintOutcome>;
}

const ReportSchema = z.object({
  valid: z.boolean(),
  problems: z
    .array(
      z.object({
        code: z.string().max(32),
        file: z.string().max(256),
        path: z.string().max(512),
        message: z.string().max(2000),
      }),
    )
    .max(5000),
});

/** The most problems stored in a row (a broken catalog can report thousands; the first ones are the useful ones). */
const MAX_STORED_PROBLEMS = 500;

const FAILURE_MESSAGES: Record<LintFailureReason, string> = {
  aictl_missing: 'The catalog checker (aictl) is not installed on the cloud server.',
  aictl_failed: 'The catalog checker (aictl) stopped with an unexpected error.',
  timeout: 'The catalog checker (aictl) did not finish within 30 seconds.',
  bad_output: 'The catalog checker (aictl) printed an answer the cloud could not read.',
};

function failure(reason: LintFailureReason, exitCode?: number): LintOutcome {
  return {
    status: 'draft',
    result: {
      error: { code: LAI_CLD_AICTL_MISSING, reason, message: FAILURE_MESSAGES[reason] },
      ...(exitCode === undefined ? {} : { exitCode }),
    },
  };
}

/** Where the binary is: the environment of the API image (`/opt/localai/bin/aictl`); read per call so tests can set it. */
export function aictlPath(env: NodeJS.ProcessEnv = process.env): string | null {
  const value = env.LOCALAI_AICTL_PATH;
  return value && value.length > 0 ? value : null;
}

/** Writes the files under `dir`; every path is re-checked against the catalog layout and must stay inside `dir`. */
async function writeCatalog(dir: string, files: readonly CatalogFile[]): Promise<void> {
  const root = resolve(dir);
  for (const file of files) {
    if (!CATALOG_PATH_RE.test(file.path)) throw new Error('refusing a path outside the catalog layout');
    const target = resolve(root, file.path);
    if (!target.startsWith(root + sep)) throw new Error('refusing a path outside the catalog directory');
    await mkdir(dirname(target), { recursive: true, mode: 0o700 });
    await writeFile(target, file.content, { encoding: 'utf8', mode: 0o600 });
  }
}

interface ExecResult {
  code: number | string | null;
  killed: boolean;
  stdout: string;
}

function run(binary: string, dir: string, timeoutMs: number): Promise<ExecResult> {
  return new Promise((resolveRun) => {
    execFile(
      binary,
      ['catalog', 'lint', '--json', dir],
      { timeout: timeoutMs, maxBuffer: LINT_MAX_OUTPUT_BYTES, env: { PATH: process.env.PATH ?? '' }, encoding: 'utf8', windowsHide: true },
      (error, stdout) => {
        if (!error) return resolveRun({ code: 0, killed: false, stdout });
        const e = error as NodeJS.ErrnoException & { killed?: boolean; code?: number | string };
        resolveRun({ code: e.code ?? null, killed: e.killed === true, stdout: typeof stdout === 'string' ? stdout : '' });
      },
    );
  });
}

/** Maps what the process did to an outcome. Exported for the unit tests of the mapping. */
export function interpretLint(run: ExecResult): LintOutcome {
  if (run.killed) return failure('timeout');
  if (run.code === 'ENOENT' || run.code === 'EACCES' || run.code === 'ENOEXEC') return failure('aictl_missing');
  if (run.code === 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER') return failure('bad_output');
  if (run.code !== 0 && run.code !== 2) return failure('aictl_failed', typeof run.code === 'number' ? run.code : undefined);

  let report: z.infer<typeof ReportSchema>;
  try {
    report = ReportSchema.parse(JSON.parse(run.stdout));
  } catch {
    return failure('bad_output', run.code);
  }
  // The exit code and the report must agree; a disagreement means something other than the linter answered.
  if (run.code === 0 && !report.valid) return failure('bad_output', 0);
  if (run.code === 2 && report.valid) return failure('bad_output', 2);
  const problems = report.problems.slice(0, MAX_STORED_PROBLEMS);
  return report.valid
    ? { status: 'valid', result: { valid: true, problems: [], exitCode: 0 } }
    : { status: 'invalid', result: { valid: false, problems, exitCode: 2 } };
}

export class AictlLinter implements CatalogLinter {
  constructor(private readonly timeoutMs: number = LINT_TIMEOUT_MS) {}

  async lint(files: readonly CatalogFile[]): Promise<LintOutcome> {
    const binary = aictlPath();
    if (!binary) return failure('aictl_missing');
    const dir = jobTempPath('-catalog');
    try {
      await mkdir(dir, { recursive: true, mode: 0o700 });
      await writeCatalog(dir, files);
      return interpretLint(await run(binary, dir, this.timeoutMs));
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }
}
