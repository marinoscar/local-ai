// `localai.aictl` (LAI-CLD-033, error): can the cloud run the catalog checker? (docs/specs/cloud-app.md sections 4.6, 5
// and 7.) Without the `aictl` binary of the API image a catalog version never leaves `draft`.
//
// The probe is `aictl --json version` (`--json` is a global flag and goes BEFORE the command, aictl.md 4.26; `aictl
// version --json` is refused as an unknown option): an argv array and no shell, an empty environment apart from PATH, a 5 second
// limit and an output ceiling. It starts a process but writes nothing and sends nothing. The output is parsed only for
// the version string, and only a version-shaped value is reported.
import { execFile } from 'node:child_process';
import { access, constants, stat } from 'node:fs/promises';
import { Inject, Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { aictlPath } from '../catalog/catalog-linter';
import { CLOUD_CODES } from './codes';
import { buildOutcome } from './doctor-outcome';
import { AICTL_PROBE_TIMEOUT_MS, LOCALAI_DOCTOR_AICTL, LOCALAI_DOCTOR_CATEGORY } from './doctor.constants';

export type AictlProbeResult =
  | { ok: true; version: string | null }
  | { ok: false; reason: 'not_configured' | 'missing' | 'not_executable' | 'timeout' | 'failed' | 'bad_output' };

/** Runs `aictl --json version` at the configured path. Replaced by a fake in unit tests. */
export interface AictlProbe {
  probe(): Promise<AictlProbeResult>;
}

const VERSION_RE = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/;
const MAX_OUTPUT_BYTES = 64 * 1024;

const REASONS: Record<Exclude<AictlProbeResult, { ok: true }>['reason'], string> = {
  not_configured: 'LOCALAI_AICTL_PATH is not set',
  missing: 'the file at LOCALAI_AICTL_PATH does not exist',
  not_executable: 'the file at LOCALAI_AICTL_PATH cannot be run',
  timeout: 'aictl did not answer within 5 seconds',
  failed: 'aictl stopped with an error',
  bad_output: 'aictl printed an answer the cloud could not read',
};

/** The real probe. */
export class ExecAictlProbe implements AictlProbe {
  constructor(
    private readonly timeoutMs: number = AICTL_PROBE_TIMEOUT_MS,
    private readonly env: NodeJS.ProcessEnv = process.env,
  ) {}

  async probe(): Promise<AictlProbeResult> {
    const binary = aictlPath(this.env);
    if (!binary) return { ok: false, reason: 'not_configured' };
    try {
      if (!(await stat(binary)).isFile()) return { ok: false, reason: 'not_executable' };
    } catch {
      return { ok: false, reason: 'missing' };
    }
    try {
      await access(binary, constants.X_OK);
    } catch {
      return { ok: false, reason: 'not_executable' };
    }

    return new Promise<AictlProbeResult>((resolve) => {
      execFile(
        binary,
        ['--json', 'version'],
        { timeout: this.timeoutMs, maxBuffer: MAX_OUTPUT_BYTES, env: { PATH: this.env.PATH ?? '' }, encoding: 'utf8', windowsHide: true },
        (error, stdout) => {
          if (error) {
            const e = error as NodeJS.ErrnoException & { killed?: boolean };
            if (e.killed === true) return resolve({ ok: false, reason: 'timeout' });
            if (e.code === 'ENOENT') return resolve({ ok: false, reason: 'missing' });
            if (e.code === 'EACCES' || e.code === 'ENOEXEC') return resolve({ ok: false, reason: 'not_executable' });
            if (e.code === 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER') return resolve({ ok: false, reason: 'bad_output' });
            return resolve({ ok: false, reason: 'failed' });
          }
          resolve(interpretVersion(stdout));
        },
      );
    });
  }
}

/**
 * The version of an `aictl --json version` answer. The documented shape is `{"cli":{"version":...},...}` (aictl.md 4.26);
 * a binary that still prints the flat `{"version":...}` is accepted too. Any other JSON object is a working binary with
 * no usable version (null); anything that is not a JSON object is unreadable.
 */
export function interpretVersion(stdout: string): AictlProbeResult {
  let parsed: unknown;
  try {
    parsed = JSON.parse(stdout);
  } catch {
    return { ok: false, reason: 'bad_output' };
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return { ok: false, reason: 'bad_output' };
  const document = parsed as { cli?: unknown; version?: unknown };
  const nested = typeof document.cli === 'object' && document.cli !== null ? (document.cli as { version?: unknown }).version : undefined;
  const version = typeof nested === 'string' ? nested : document.version;
  return { ok: true, version: typeof version === 'string' && VERSION_RE.test(version) ? version : null };
}

@Injectable()
export class AictlDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.aictl';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'Catalog checker (aictl)';
  // The probe is bounded at 5 s; the ceiling leaves room for the file checks.
  readonly timeoutMs = AICTL_PROBE_TIMEOUT_MS + 2_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    @Inject(LOCALAI_DOCTOR_AICTL) private readonly aictl: AictlProbe,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async run(): Promise<DoctorCheckOutcome> {
    const result = await this.aictl.probe();
    if (result.ok) {
      return buildOutcome({
        status: 'pass',
        detail: result.version ? `aictl ${result.version} answered` : 'aictl answered',
        data: result.version ? { version: result.version } : {},
      });
    }
    return buildOutcome({ status: 'fail', code: CLOUD_CODES.aictlMissing, found: REASONS[result.reason], data: { reason: result.reason } });
  }
}
