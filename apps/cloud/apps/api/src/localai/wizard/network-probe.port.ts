// The cloud's eyes on the public side of a node (docs/specs/cloud-app.md section 4.8): DNS, the TLS certificate and two
// HTTP requests, run from the API container. A port, so tests inject a fake and no check ever touches the network there.
//
// Contract for every implementation: each call ends within `timeoutMs` (10 s for the checklist), answers with data only,
// and its errors carry no request header, no URL query and no response body: the caller turns any failure into a fixed
// plain sentence, so nothing a probe throws can reach a stored `detail`.

/** What the TLS handshake with `host:443` showed. */
export interface TlsCertInfo {
  /** The chain verified against the system roots for the name asked (SNI). */
  authorized: boolean;
  /** The organisation of the issuer (`Let's Encrypt`), when the certificate names one. */
  issuerOrganization: string | null;
  issuerCommonName: string | null;
  /** The last instant the certificate is valid. */
  validTo: Date;
}

export interface HttpProbeResponse {
  status: number;
  /** At most 64 KiB of the body. */
  body: string;
}

export interface HttpProbeOptions {
  timeoutMs: number;
  headers?: Readonly<Record<string, string>>;
}

export interface NetworkProbe {
  /** IPv4 addresses of `host`. Rejects when the name does not exist. */
  resolve4(host: string, options: { timeoutMs: number }): Promise<string[]>;
  tlsCheck(host: string, port: number, options: { timeoutMs: number }): Promise<TlsCertInfo>;
  /** `GET url` over HTTPS, redirects not followed. A non-2xx answer resolves; only a transport failure rejects. */
  httpGet(url: string, options: HttpProbeOptions): Promise<HttpProbeResponse>;
}

/** Nest injection token of the port. */
export const NETWORK_PROBE = Symbol('NETWORK_PROBE');

/** Each automatic check gives up after this long (spec section 4.8). */
export const CHECK_TIMEOUT_MS = 10_000;
