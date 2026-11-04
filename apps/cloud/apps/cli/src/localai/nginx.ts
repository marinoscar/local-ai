// The shared proxy's vhost files and its reload (docs/specs/cloud-app.md section 4.10; runbook 05 part 7). Every change
// is: back up, write, `docker exec <proxy> nginx -t`, reload; and a failed `nginx -t` puts the previous file back and
// re-tests, so the proxy is never left un-reloadable. All paths derive from a validated host and a fixed directory.
//
// `nginx -t` output is never copied into a detail: it echoes configuration lines (which can hold a tailnet address).
import { promises as fs } from 'node:fs';
import { join } from 'node:path';

import type { CommandRunner } from './vps-process.js';
import { CONTAINER_NAME_RE, VPS_HOST_RE, VpsInputError } from './vps-host.js';

export interface NginxOptions {
  runner: CommandRunner;
  /** The shared proxy directory (`/opt/infra/proxy`). */
  proxyDir: string;
  /** The proxy container (`proxy-nginx`). */
  container: string;
  now?: () => Date;
  /** How many `.bak.` files to keep per vhost. */
  keepBackups?: number;
}

/** A change nginx would not accept. The previous state was restored (`restored`) or could not be (`restored: false`). */
export class NginxRefusedError extends Error {
  constructor(
    readonly restored: boolean,
    message: string,
  ) {
    super(message);
    this.name = 'NginxRefusedError';
  }
}

export type ApplyOutcome = 'unchanged' | 'applied';
export type RemoveOutcome = 'absent' | 'removed';

const stamp = (now: Date) => now.toISOString().replace(/[-:.]/g, '').slice(0, 15);

export class NginxProxy {
  private readonly runner: CommandRunner;
  private readonly dir: string;
  private readonly container: string;
  private readonly now: () => Date;
  private readonly keepBackups: number;

  constructor(options: NginxOptions) {
    if (!CONTAINER_NAME_RE.test(options.container)) throw new VpsInputError('The proxy container name is not valid.');
    if (!options.proxyDir.startsWith('/') || options.proxyDir.includes('..') || /[\0\n\r]/.test(options.proxyDir)) throw new VpsInputError('The proxy directory is not valid.');
    this.runner = options.runner;
    this.dir = join(options.proxyDir, 'nginx', 'conf.d');
    this.container = options.container;
    this.now = options.now ?? (() => new Date());
    this.keepBackups = options.keepBackups ?? 5;
  }

  /** The vhost file of a host. Refuses anything that is not a publishable host name. */
  vhostPath(host: string): string {
    if (!VPS_HOST_RE.test(host)) throw new VpsInputError('The host name is not one this agent may publish.');
    return join(this.dir, `${host}.conf`);
  }

  /** The bootstrap file some operators keep beside the final one. */
  private bootstrapPath(host: string): string {
    if (!VPS_HOST_RE.test(host)) throw new VpsInputError('The host name is not one this agent may publish.');
    return join(this.dir, `${host}.bootstrap.conf`);
  }

  async read(host: string): Promise<string | null> {
    try {
      return await fs.readFile(this.vhostPath(host), 'utf8');
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
      throw error;
    }
  }

  /** `nginx -t` in the proxy container. */
  async test(): Promise<boolean> {
    const result = await this.runner.run('docker', ['exec', this.container, 'nginx', '-t'], { timeoutMs: 30_000 });
    return result.exitCode === 0;
  }

  async reload(): Promise<boolean> {
    const result = await this.runner.run('docker', ['exec', this.container, 'nginx', '-s', 'reload'], { timeoutMs: 30_000 });
    return result.exitCode === 0;
  }

  /**
   * Writes `content` as the vhost of `host`. Unchanged when the file already holds exactly that (no backup, no reload).
   * Otherwise: backup to `<file>.bak.<ts>`, atomic write, `nginx -t`, reload; on a refusal the previous file (or its
   * absence) is restored, tested again and reloaded, and `NginxRefusedError` is thrown.
   */
  async apply(host: string, content: string): Promise<ApplyOutcome> {
    const target = this.vhostPath(host);
    const previous = await this.read(host);
    if (previous === content) return 'unchanged';

    const backup = previous === null ? null : `${target}.bak.${stamp(this.now())}`;
    if (backup !== null) await fs.copyFile(target, backup);
    await fs.mkdir(this.dir, { recursive: true });
    const temp = `${target}.tmp-${stamp(this.now())}`;
    await fs.writeFile(temp, content, { mode: 0o644 });
    await fs.rename(temp, target);

    if (!(await this.test())) {
      const restored = await this.restore(target, backup);
      throw new NginxRefusedError(restored, 'nginx did not accept the new vhost file.');
    }
    if (!(await this.reload())) {
      const restored = await this.restore(target, backup);
      throw new NginxRefusedError(restored, 'nginx did not reload with the new vhost file.');
    }
    await this.prune(target);
    return 'applied';
  }

  /**
   * Takes the vhost files of `host` out of service (the final file and any bootstrap file), keeping each as
   * `<file>.removed.<ts>` so the change can be undone by hand. `absent` when there was nothing to remove.
   */
  async remove(host: string): Promise<RemoveOutcome> {
    const moved: Array<{ from: string; to: string }> = [];
    for (const from of [this.vhostPath(host), this.bootstrapPath(host)]) {
      try {
        await fs.access(from);
      } catch {
        continue;
      }
      const to = `${from}.removed.${stamp(this.now())}`;
      await fs.rename(from, to);
      moved.push({ from, to });
    }
    if (moved.length === 0) return 'absent';

    if (!(await this.test()) || !(await this.reload())) {
      let ok = true;
      for (const { from, to } of moved) {
        try {
          await fs.rename(to, from);
        } catch {
          ok = false;
        }
      }
      if (ok) await this.test(); // keep the proxy testable; the result is not needed here
      throw new NginxRefusedError(ok, 'nginx did not accept the removal.');
    }
    return 'removed';
  }

  /** Puts the previous file back (or removes the new one), then tests and reloads. Returns whether nginx is healthy again. */
  private async restore(target: string, backup: string | null): Promise<boolean> {
    try {
      if (backup === null) await fs.rm(target, { force: true });
      else await fs.copyFile(backup, target);
    } catch {
      return false;
    }
    if (!(await this.test())) return false;
    return this.reload();
  }

  private async prune(target: string): Promise<void> {
    try {
      const base = `${target.split('/').pop()}.bak.`;
      const names = (await fs.readdir(this.dir)).filter((name) => name.startsWith(base)).sort();
      for (const name of names.slice(0, Math.max(0, names.length - this.keepBackups))) await fs.rm(join(this.dir, name), { force: true });
    } catch {
      // Pruning is housekeeping: never fail a publish for it.
    }
  }
}
