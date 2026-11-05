// The gateway's HTTP client towards a node's llama-server (docs/specs/gateway.md section 3.5). It does ONE thing: send
// one request to one node and hand back the status, the `Content-Type` and the unread body. Retrying, classifying the
// status and reading the body belong to the attempt loop (`../attempts.ts`) and the callers.
//
// MODE. `public` (`https://<publicHost><path>`) or `tailnet` (the node's tailnet address, Pattern A plain HTTP on 18080,
// Pattern B TLS with SNI), both with the certificate verified against the system CA bundle. The mode is which
// `TargetResolver` the client was given (`targets.ts`); `createUpstreamTransport` chooses it from the environment.
//
// WHAT GOES UPSTREAM. Exactly: `Authorization: Bearer <node llama key>`, `Content-Type`, `Accept`, `X-Request-ID` and
// `User-Agent: localai-gateway/<version>`. Nothing of the client's request is copied (no `Authorization`, no cookie, no
// `X-Forwarded-*`): the headers are built from scratch below, from values the gateway itself decided. What comes back
// to the caller is `Content-Type` only.
//
// SSRF. The origin is built from the node's row in `localai_nodes` (admin data), never from the request, and each piece
// is validated first (`targets.ts`).
//
// SECRETS. The node key is only ever placed in the `Authorization` header of the outgoing call. Errors thrown here carry
// a reason and a Node error CODE, never the key, the URL or the underlying message.
import type { Readable } from 'node:stream';

import { Logger } from '@nestjs/common';
import { Agent, request as undiciRequest } from 'undici';

import { classifyTransportError, transportErrorCode, UpstreamAbortedError, UpstreamTransportError } from './errors';
import { isValidPublicHost, PublicTargets, type TargetResolver, type TargetSubject, type UpstreamRoute, type UpstreamTarget } from './targets';

export { isValidPublicHost };

/** What one outgoing call needs. Everything here is decided by the gateway, none of it is copied from the client. */
export interface UpstreamCall {
  nodeId: string;
  /** `localai_nodes.publicHost`. */
  publicHost: string;
  /** How to reach the node over the tailnet (`tailnet` mode only; never logged). */
  route?: UpstreamRoute;
  /** `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`. */
  path: string;
  /** The already re-serialised body. */
  body: Buffer;
  /** `application/json` for the JSON endpoints, `multipart/form-data; boundary=...` for a transcription. */
  contentType: string;
  /** `application/json`, or `text/event-stream` for a stream. */
  accept: string;
  requestId: string;
  /** The node's llama key (plaintext, from `NodeKeyProvider`). Used for the `Authorization` header only. */
  apiKey: string;
  /** Aborts the call and closes the socket (client gone, total timeout). */
  signal: AbortSignal;
  /** `gateway.timeouts.connectMs`. */
  connectMs: number;
  /** Time to the response headers (`gateway.timeouts.firstByteMs`, or the model policy's). */
  firstByteMs: number;
  /** Time between two body chunks (`gateway.timeouts.idleMs`). */
  idleMs: number;
}

/** The answer's head and its unread body. The caller owns the body: read it to the end or call {@link destroy}. */
export interface UpstreamResponse {
  status: number;
  /** The upstream `Content-Type`, the only header that is forwarded to the client. */
  contentType: string | null;
  body: Readable;
  /** Milliseconds from the start of the call to the response headers. */
  ttfbMs: number;
  /** Closes the body and the socket. Safe to call more than once. */
  destroy(): void;
}

/** The seam GW-10 plugs the tailnet transport into, and the specs plug a fake into. */
export interface UpstreamTransport {
  /**
   * @throws UpstreamTransportError the node could not be reached or did not answer in time (`connect` | `tls` | `timeout`).
   * @throws UpstreamAbortedError `call.signal` fired.
   */
  call(call: UpstreamCall): Promise<UpstreamResponse>;
  /**
   * Can this node be called at all in the transport's mode? Absent means yes. The attempt loop leaves a node that
   * cannot out of the candidates (it is not an attempt and not a failure).
   */
  routable?(node: Pick<UpstreamCall, 'nodeId' | 'publicHost' | 'route'>): Promise<boolean>;
}

/** Injection token of the {@link UpstreamTransport}. */
export const UPSTREAM_TRANSPORT = Symbol('LOCALAI_GATEWAY_UPSTREAM_TRANSPORT');

export const UPSTREAM_KEEP_ALIVE_MS = 30_000;
export const UPSTREAM_CONNECTIONS = 32;
const MAX_AGENTS = 64;

export interface UpstreamClientOptions {
  /** `User-Agent: localai-gateway/<version>`. */
  version: string;
  /**
   * The origin for a host. The default is `https://<host>` (public mode). A spec replaces it to reach a fake llama
   * server on `http://127.0.0.1:<port>`; production never does. Ignored when `targets` is given.
   */
  resolveOrigin?: (publicHost: string) => string;
  /** The mode: where a call goes. Default: public mode over `resolveOrigin`. */
  targets?: TargetResolver;
  /** undici connections per origin; default {@link UPSTREAM_CONNECTIONS}. Only the load-test flag raises it. */
  connections?: number;
  /** Extra trusted CA certificates for TLS targets (a spec's self-signed CA; production uses the system bundle only). */
  tlsCa?: string | Buffer | Array<string | Buffer>;
}

/** undici `Agent` per origin (and TLS server name, and timeout set), keep-alive 30 s, 32 connections, 5 s connect timeout, certificate verified. */
export class UpstreamClient implements UpstreamTransport {
  private readonly logger = new Logger(UpstreamClient.name);
  private readonly agents = new Map<string, Agent>();
  private readonly targets: TargetResolver;
  private readonly tlsCa: UpstreamClientOptions['tlsCa'];
  private readonly userAgent: string;
  private readonly connections: number;

  constructor(options: UpstreamClientOptions) {
    this.userAgent = `localai-gateway/${options.version}`;
    this.targets = options.targets ?? new PublicTargets(options.resolveOrigin, (line) => this.logger.warn(line));
    this.tlsCa = options.tlsCa;
    this.connections = options.connections ?? UPSTREAM_CONNECTIONS;
  }

  async routable(node: TargetSubject): Promise<boolean> {
    return this.targets.routable ? this.targets.routable(node) : true;
  }

  async call(call: UpstreamCall): Promise<UpstreamResponse> {
    if (call.signal.aborted) throw new UpstreamAbortedError();
    const target = await this.targets.resolve({ nodeId: call.nodeId, publicHost: call.publicHost, route: call.route });
    // A node that cannot be addressed in this mode is never dialled; it counts as unreachable.
    if (!target) throw new UpstreamTransportError('connect', 'NO_TARGET');
    if (call.signal.aborted) throw new UpstreamAbortedError();
    const startedAt = performance.now();
    try {
      const response = await undiciRequest(`${target.origin}${call.path}`, {
        method: 'POST',
        headers: {
          // `host` only when the target names a different host than the address it dials (tailnet Pattern B).
          ...(target.hostHeader ? { host: target.hostHeader } : {}),
          authorization: `Bearer ${call.apiKey}`,
          'content-type': call.contentType,
          accept: call.accept,
          'x-request-id': call.requestId,
          'user-agent': this.userAgent,
        },
        body: call.body,
        dispatcher: this.agentFor(target, call),
        signal: call.signal,
        // `request()` never follows redirects, which is what is wanted: a 3xx from a node is a misconfiguration and the
        // attempt loop treats it like a 5xx; the node's key is never re-sent to another location.
      });
      const contentType = response.headers['content-type'];
      const body = response.body;
      return {
        status: response.statusCode,
        contentType: typeof contentType === 'string' ? contentType : Array.isArray(contentType) ? (contentType[0] ?? null) : null,
        body,
        ttfbMs: Math.round(performance.now() - startedAt),
        destroy: () => {
          // Destroying an unread body makes undici emit an "aborted" error: nobody is listening for it any more.
          body.on('error', () => undefined);
          if (!body.destroyed) body.destroy();
        },
      };
    } catch (error) {
      const kind = classifyTransportError(error);
      if (kind === 'aborted') throw new UpstreamAbortedError();
      throw new UpstreamTransportError(kind, transportErrorCode(error));
    }
  }

  /**
   * One agent per origin and timeout set. undici fixes `connect.timeout`, `headersTimeout` (= the first-byte timeout) and
   * `bodyTimeout` (= the idle timeout) per AGENT and ignores them on a single request, so the timeouts are part of the
   * key. The set of distinct values is small (the settings plus the per-model first-byte overrides); the map is capped
   * anyway, closing the oldest agent (its in-flight requests finish: `close()` waits for them).
   */
  private agentFor(target: UpstreamTarget, call: Pick<UpstreamCall, 'connectMs' | 'firstByteMs' | 'idleMs'>): Agent {
    // The TLS server name is part of the key: Pattern B dials an address, so two machines behind one address (or one
    // machine renamed) must not share a connection verified for another name.
    const key = `${target.origin}|${target.servername ?? ''}|${call.connectMs}|${call.firstByteMs}|${call.idleMs}`;
    let agent = this.agents.get(key);
    if (!agent) {
      if (this.agents.size >= MAX_AGENTS) {
        const oldest = this.agents.keys().next().value as string;
        const old = this.agents.get(oldest);
        this.agents.delete(oldest);
        void old?.close().catch(() => undefined);
      }
      agent = new Agent({
        keepAliveTimeout: UPSTREAM_KEEP_ALIVE_MS,
        keepAliveMaxTimeout: UPSTREAM_KEEP_ALIVE_MS,
        connections: this.connections,
        connect: {
          timeout: call.connectMs,
          ...(target.servername ? { servername: target.servername } : {}),
          ...(this.tlsCa ? { ca: this.tlsCa } : {}),
        },
        headersTimeout: call.firstByteMs,
        bodyTimeout: call.idleMs,
      });
      this.agents.set(key, agent);
    }
    return agent;
  }

  /** Closes every pooled connection (application shutdown, end of a spec). */
  async close(): Promise<void> {
    const agents = [...this.agents.values()];
    this.agents.clear();
    await Promise.allSettled(agents.map((agent) => agent.close()));
  }
}
