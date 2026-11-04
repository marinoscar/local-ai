// What the public resolvers answer (docs/specs/cloud-app.md section 4.10 steps 1 and 2): `dig` against fixed public
// resolvers, as a fixed argument array. Used to learn the VPS's public address, to skip an UPSERT that is already
// true, and to wait until a new record is visible.
import type { CommandRunner } from './vps-process.js';
import { DNS_NAME_RE, VPS_HOST_RE, VpsInputError, isPublicIpv4 } from './vps-host.js';

/** Well-known public resolvers. Not lab values. */
export const PUBLIC_RESOLVERS: readonly string[] = ['1.1.1.1', '8.8.8.8'];

export interface DnsChecker {
  /** The A records `name` has at one resolver (empty when none or on a resolver failure). */
  resolveA(name: string, resolver: string): Promise<string[]>;
}

export class DigChecker implements DnsChecker {
  constructor(private readonly runner: CommandRunner) {}

  async resolveA(name: string, resolver: string): Promise<string[]> {
    if (!VPS_HOST_RE.test(name) && !DNS_NAME_RE.test(name)) throw new VpsInputError('The name to look up is not a valid DNS name.');
    if (!PUBLIC_RESOLVERS.includes(resolver)) throw new VpsInputError('The resolver is not one of the configured public resolvers.');
    const result = await this.runner.run('dig', ['+short', '+time=3', '+tries=1', `@${resolver}`, name, 'A'], { timeoutMs: 15_000 });
    if (result.exitCode !== 0) return [];
    return result.stdout
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => /^(\d{1,3}\.){3}\d{1,3}$/.test(line));
  }
}

/** The public IPv4 address `name` resolves to at the first resolver that answers with one, or null. */
export async function vpsPublicAddress(dns: DnsChecker, name: string): Promise<string | null> {
  for (const resolver of PUBLIC_RESOLVERS) {
    const found = (await dns.resolveA(name, resolver)).find(isPublicIpv4);
    if (found) return found;
  }
  return null;
}

/** Whether EVERY public resolver answers exactly `ip` for `name` (a second, stale address counts as not yet). */
export async function allResolversAnswer(dns: DnsChecker, name: string, ip: string): Promise<boolean> {
  for (const resolver of PUBLIC_RESOLVERS) {
    const answers = await dns.resolveA(name, resolver);
    if (answers.length !== 1 || answers[0] !== ip) return false;
  }
  return true;
}

/** Whether ANY public resolver still answers something for `name`. */
export async function anyResolverAnswers(dns: DnsChecker, name: string): Promise<boolean> {
  for (const resolver of PUBLIC_RESOLVERS) if ((await dns.resolveA(name, resolver)).length > 0) return true;
  return false;
}
