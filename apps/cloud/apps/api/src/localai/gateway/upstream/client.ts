// The gateway's HTTP client towards a node's llama-server (docs/specs/gateway.md section 3.5). It does ONE thing: send
// one request to one node and hand back the status, the `Content-Type` and the unread body. Retrying, classifying the
// status and reading the body belong to the attempt loop (`../attempts.ts`) and the callers.
//
// MODE. Only `public` exists here (`https://<publicHost><path>`, certificate verified against the system CA bundle). The
// `tailnet` mode is GW-10: it implements the same `UpstreamTransport` interface and is chosen by `createUpstreamTransport`.
//
// WHAT GOES UPSTREAM. Exactly: `Authorization: Bearer <node llama key>`, `Content-Type`, `Accept`, `X-Request-ID` and
// `User-Agent: localai-gateway/<version>`. Nothing of the client's request is copied (no `Authorization`, no cookie, no
// `X-Forwarded-*`): the headers are built from scratch below, from values the gateway itself decided. What comes back
// to the caller is `Content-Type` only.
//
// SSRF. The origin is built from the node's `publicHost` (admin data from `localai_nodes`), never from the request. The
// host is validated as a plain DNS name (no scheme, port, path, userinfo or IP-literal tricks) before it is used.
//
// SECRETS. The node key is only ever placed in the `Authorization` header of the outgoing call. Errors thrown here carry
// a reason and a Node error CODE, never the key, the URL or the underlying message.
import type { Readable } from 'node:stream';

import { Logger } from '@nestjs/common';
import { Agent, request as undiciRequest } from 'undici';

import { classifyTransportError, transportErrorCode, UpstreamAbortedError, UpstreamTransportError } from './errors';

/** What one outgoing call needs. Everything here is decided by the gateway, none of it is copied from the client. */
export interface UpstreamCall {
  nodeId: string;
  /** `localai_nodes.publicHost`. */
  publicHost: string;
  /** `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`. */
  path: string;
  /** The already re-serialised JSON body. */
  body: Buffer;
  /** `application/json` for the JSON endpoints. */
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
}

/** Injection token of the {@link UpstreamTransport}. */
export const UPSTREAM_TRANSPORT = Symbol('LOCALAI_GATEWAY_UPSTREAM_TRANSPORT');

export const UPSTREAM_KEEP_ALIVE_MS = 30_000;
export const UPSTREAM_CONNECTIONS = 32;
const MAX_AGENTS = 64;

/** A DNS name: labels of letters, digits and hyphens. No port, no path, no userinfo, no brackets, no IPv6 literal. */
const HOST_NAME = /^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;

export function isValidPublicHost(host: string): boolean {
  return HOST_NAME.test(host);
}

export interface UpstreamClientOptions {
  /** `User-Agent: localai-gateway/<version>`. */
  version: string;
  /**
   * The origin for a host. The default is `https://<host>` (public mode). A spec replaces it to reach a fake llama
   * server on `http://127.0.0.1:<port>`; production never does.
   */
  resolveOrigin?: (publicHost: string) => string;
}

/** Public mode: undici `Agent` per origin (and timeout set), keep-alive 30 s, 32 connections, 5 s connect timeout, certificate verified. */
export class UpstreamClient implements UpstreamTransport {
  private readonly logger = new Logger(UpstreamClient.name);
  private readonly agents = new Map<string, Agent>();
  private readonly resolveOrigin: (publicHost: string) => string;
  private readonly userAgent: string;

  constructor(options: UpstreamClientOptions) {
    this.userAgent = `localai-gateway/${options.version}`;
    this.resolveOrigin = options.resolveOrigin ?? ((host) => `https://${host}`);
  }

  async call(call: UpstreamCall): Promise<UpstreamResponse> {
    if (call.signal.aborted) throw new UpstreamAbortedError();
    if (!isValidPublicHost(call.publicHost)) {
      // A host an administrator typed that cannot be a DNS name is never dialled; the node counts as unreachable.
      this.logger.warn(`node ${call.nodeId} has an invalid public host, not calling it`);
      throw new UpstreamTransportError('connect', 'INVALID_HOST');
    }
    const origin = this.resolveOrigin(call.publicHost);
    const startedAt = performance.now();
    try {
      const response = await undiciRequest(`${origin}${call.path}`, {
        method: 'POST',
        headers: {
          authorization: `Bearer ${call.apiKey}`,
          'content-type': call.contentType,
          accept: call.accept,
          'x-request-id': call.requestId,
          'user-agent': this.userAgent,
        },
        body: call.body,
        dispatcher: this.agentFor(origin, call),
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
  private agentFor(origin: string, call: Pick<UpstreamCall, 'connectMs' | 'firstByteMs' | 'idleMs'>): Agent {
    const key = `${origin}|${call.connectMs}|${call.firstByteMs}|${call.idleMs}`;
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
        connections: UPSTREAM_CONNECTIONS,
        connect: { timeout: call.connectMs },
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
