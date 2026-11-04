// Where one call to a node goes (docs/specs/gateway.md section 3.5): the pure target builders of the two upstream modes
// and the resolvers `UpstreamClient` asks. Nothing here does I/O except reading the tailnet suffix setting.
//
//   public   https://<publicHost><path>                   certificate verified against the system CA bundle
//   tailnet  Pattern A  http://<tailnetIp>:18080<path>
//            Pattern B  https://<tailnetIp>:443<path>      SNI and `Host` = <tailnetMachine>.<gateway.tailnetSuffix>,
//                                                          certificate verified against the system CA bundle
//
// SSRF. A target is built ONLY from the node's row in `localai_nodes` (admin data) and the `gateway.tailnetSuffix`
// setting; nothing of the client's request reaches it. Each piece is validated before it is used: the public host as a
// DNS name, the tailnet address as an IP literal (no zone id), the machine as one DNS label, the suffix as
// `<tailnet>.ts.net`.
//
// SECRETS. A `tailnetIp` stays in memory: it is part of the target the client dials and of nothing else. No log line,
// error or response header carries it; the warnings below name the node id only.
import { isIP } from 'node:net';

export type ServePattern = 'A' | 'B';

/** What the routing table knows about how to reach a node besides its public host (never exported, never logged). */
export interface UpstreamRoute {
  servePattern: ServePattern;
  tailnetIp: string | null;
  tailnetMachine: string | null;
}

/** The node a call is for. */
export interface TargetSubject {
  nodeId: string;
  publicHost: string;
  route?: UpstreamRoute;
}

export interface UpstreamTarget {
  /** `scheme://host:port`, no path. */
  origin: string;
  /** Replaces the `Host` header (Pattern B: the machine's name on the tailnet, while the socket goes to the IP). */
  hostHeader?: string;
  /** The TLS server name: what the handshake presents as SNI and what the certificate is checked against. */
  servername?: string;
}

export const TAILNET_PATTERN_A_PORT = 18080;
export const TAILNET_PATTERN_B_PORT = 443;

export interface TailnetPorts {
  a: number;
  b: number;
}

export const DEFAULT_TAILNET_PORTS: TailnetPorts = { a: TAILNET_PATTERN_A_PORT, b: TAILNET_PATTERN_B_PORT };

/** A DNS name: labels of letters, digits and hyphens. No port, no path, no userinfo, no brackets, no IPv6 literal. */
const HOST_NAME = /^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;
const DNS_LABEL = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
/** The shape the `gateway.tailnetSuffix` setting accepts (`localai.settings.ts`). */
export const TAILNET_SUFFIX = /^[a-z0-9-]{1,63}\.ts\.net$/;

export function isValidPublicHost(host: string): boolean {
  return HOST_NAME.test(host);
}

/** Why a node cannot be reached in tailnet mode. Logged as a word, never with a value. */
export type TargetRefusal = 'no_tailnet_ip' | 'invalid_tailnet_ip' | 'no_tailnet_machine' | 'invalid_tailnet_machine' | 'no_tailnet_suffix';

export type TargetResult = { ok: true; target: UpstreamTarget } | { ok: false; reason: TargetRefusal };

function ipHost(ip: string): string | null {
  if (ip.includes('%')) return null;
  const version = isIP(ip);
  if (version === 4) return ip;
  if (version === 6) return `[${ip}]`;
  return null;
}

/**
 * Pure: the target of a node in tailnet mode. Pattern A needs the address only; Pattern B also needs the machine name
 * and the tailnet suffix, which together make the name the node's certificate is issued for.
 */
export function buildTailnetTarget(route: UpstreamRoute, suffix: string | null, ports: TailnetPorts = DEFAULT_TAILNET_PORTS): TargetResult {
  const rawIp = route.tailnetIp?.trim() ?? '';
  if (rawIp === '') return { ok: false, reason: 'no_tailnet_ip' };
  const host = ipHost(rawIp);
  if (host === null) return { ok: false, reason: 'invalid_tailnet_ip' };

  if (route.servePattern === 'A') return { ok: true, target: { origin: `http://${host}:${ports.a}` } };

  const machine = route.tailnetMachine?.trim() ?? '';
  if (machine === '') return { ok: false, reason: 'no_tailnet_machine' };
  if (!DNS_LABEL.test(machine)) return { ok: false, reason: 'invalid_tailnet_machine' };
  if (suffix === null || !TAILNET_SUFFIX.test(suffix)) return { ok: false, reason: 'no_tailnet_suffix' };
  const name = `${machine}.${suffix}`;
  return { ok: true, target: { origin: `https://${host}:${ports.b}`, hostHeader: name, servername: name } };
}

/** What `UpstreamClient` asks. The mode is which implementation it was given. */
export interface TargetResolver {
  /** The target of the call, or null when the node cannot be reached in this mode. */
  resolve(subject: TargetSubject): Promise<UpstreamTarget | null>;
  /** Can the node be reached at all in this mode? A node that cannot is left out of the candidates, not tried and failed. */
  routable?(subject: TargetSubject): Promise<boolean>;
}

/** Public mode: `https://<publicHost>`, or whatever origin a spec maps the host to. */
export class PublicTargets implements TargetResolver {
  constructor(
    private readonly resolveOrigin: (publicHost: string) => string = (host) => `https://${host}`,
    private readonly warn: (line: string) => void = () => undefined,
  ) {}

  async resolve(subject: TargetSubject): Promise<UpstreamTarget | null> {
    if (!isValidPublicHost(subject.publicHost)) {
      // A host an administrator typed that cannot be a DNS name is never dialled; the node counts as unreachable.
      this.warn(`node ${subject.nodeId} has an invalid public host, not calling it`);
      return null;
    }
    return { origin: this.resolveOrigin(subject.publicHost) };
  }
}

/** The `gateway.tailnetSuffix` setting, read on demand (cached by the implementation). */
export interface TailnetSuffixSource {
  read(): Promise<string | null>;
}

export interface TailnetTargetsOptions {
  suffix: TailnetSuffixSource;
  ports?: TailnetPorts;
  warn?: (line: string) => void;
}

/** Tailnet mode. A node that cannot be addressed is reported once (its id and the reason) and left out of the candidates. */
export class TailnetTargets implements TargetResolver {
  private readonly ports: TailnetPorts;
  private readonly warn: (line: string) => void;
  private readonly warned = new Set<string>();

  constructor(private readonly options: TailnetTargetsOptions) {
    this.ports = options.ports ?? DEFAULT_TAILNET_PORTS;
    this.warn = options.warn ?? (() => undefined);
  }

  async resolve(subject: TargetSubject): Promise<UpstreamTarget | null> {
    const result = await this.build(subject);
    return result.ok ? result.target : null;
  }

  async routable(subject: TargetSubject): Promise<boolean> {
    const result = await this.build(subject);
    if (result.ok) {
      this.warned.delete(subject.nodeId);
      return true;
    }
    if (!this.warned.has(subject.nodeId)) {
      this.warned.add(subject.nodeId);
      this.warn(`node ${subject.nodeId} is left out in tailnet mode: ${result.reason}`);
    }
    return false;
  }

  private async build(subject: TargetSubject): Promise<TargetResult> {
    const route = subject.route;
    if (!route) return { ok: false, reason: 'no_tailnet_ip' };
    // Pattern A never reads the suffix: do not touch the settings for it.
    const suffix = route.servePattern === 'B' ? await this.options.suffix.read().catch(() => null) : null;
    return buildTailnetTarget(route, suffix, this.ports);
  }
}
