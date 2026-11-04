// Subprocesses of the stack-agent (docs/specs/cloud-app.md section 4.10): `docker`, `dig`. Every one goes through
// `execFile` with a FIXED argument array and no shell, so a host name can never become a command. The child gets a
// minimal environment: the worker's own environment holds its `nod_` token and must not reach a child.
import { execFile } from 'node:child_process';

export interface CommandResult {
  exitCode: number;
  stdout: string;
  stderr: string;
}

export interface CommandOptions {
  timeoutMs?: number;
}

export interface CommandRunner {
  /** Never rejects for a non-zero exit: the code is in the result. Rejects only when the program cannot be started. */
  run(file: string, args: readonly string[], options?: CommandOptions): Promise<CommandResult>;
}

/** The only variables a child sees. */
function childEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { PATH: process.env.PATH ?? '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', LANG: 'C' };
  for (const name of ['HOME', 'DOCKER_HOST', 'DOCKER_CONFIG'] as const) {
    const value = process.env[name];
    if (value !== undefined) env[name] = value;
  }
  return env;
}

export class ExecFileRunner implements CommandRunner {
  run(file: string, args: readonly string[], options: CommandOptions = {}): Promise<CommandResult> {
    return new Promise((resolve, reject) => {
      execFile(
        file,
        [...args],
        { shell: false, env: childEnv(), timeout: options.timeoutMs ?? 120_000, maxBuffer: 1024 * 1024, windowsHide: true },
        (error, stdout, stderr) => {
          if (error === null) return resolve({ exitCode: 0, stdout: String(stdout), stderr: String(stderr) });
          const code = (error as NodeJS.ErrnoException & { code?: string | number }).code;
          if (typeof code === 'number') return resolve({ exitCode: code, stdout: String(stdout), stderr: String(stderr) });
          if (code === 'ENOENT') return reject(new Error(`The program ${file} is not installed on this machine.`));
          // Timeout (killed) or a signal: a non-zero result, not an exception.
          resolve({ exitCode: 124, stdout: String(stdout), stderr: String(stderr) });
        },
      );
    });
  }
}
