// The real `NetworkProbe`: node's `dns`, `tls` and `fetch`. Used in production; tests inject a fake.
//
// Only HTTPS, only a host that `PUBLIC_HOST_RE` accepts (the callers check; `httpGet` refuses anything but https:), and
// redirects are never followed, so a node's vhost cannot bounce the cloud to an internal address.
import { Resolver } from 'node:dns/promises';
import { connect } from 'node:tls';
import { Injectable } from '@nestjs/common';

import type { HttpProbeOptions, HttpProbeResponse, NetworkProbe, TlsCertInfo } from './network-probe.port';

const MAX_BODY_BYTES = 64 * 1024;

@Injectable()
export class NodeNetworkProbe implements NetworkProbe {
  async resolve4(host: string, options: { timeoutMs: number }): Promise<string[]> {
    const resolver = new Resolver({ timeout: options.timeoutMs, tries: 1 });
    return resolver.resolve4(host);
  }

  tlsCheck(host: string, port: number, options: { timeoutMs: number }): Promise<TlsCertInfo> {
    return new Promise<TlsCertInfo>((resolve, reject) => {
      // Read the certificate even when it does not verify: the check reports WHY it is not good.
      const socket = connect({ host, port, servername: host, rejectUnauthorized: false, timeout: options.timeoutMs });
      const fail = (message: string) => {
        socket.destroy();
        reject(new Error(message));
      };
      socket.once('timeout', () => fail('tls timeout'));
      socket.once('error', () => fail('tls error'));
      socket.once('secureConnect', () => {
        const certificate = socket.getPeerCertificate();
        const authorized = socket.authorized;
        socket.end();
        if (!certificate || !certificate.valid_to) {
          reject(new Error('no certificate'));
          return;
        }
        const organization = certificate.issuer?.O;
        const commonName = certificate.issuer?.CN;
        resolve({
          authorized,
          issuerOrganization: Array.isArray(organization) ? (organization[0] ?? null) : (organization ?? null),
          issuerCommonName: Array.isArray(commonName) ? (commonName[0] ?? null) : (commonName ?? null),
          validTo: new Date(certificate.valid_to),
        });
      });
    });
  }

  async httpGet(url: string, options: HttpProbeOptions): Promise<HttpProbeResponse> {
    if (!url.startsWith('https://')) throw new Error('only https is probed');
    const response = await fetch(url, {
      method: 'GET',
      redirect: 'manual',
      headers: options.headers ? { ...options.headers } : undefined,
      signal: AbortSignal.timeout(options.timeoutMs),
    });
    const reader = response.body?.getReader();
    const chunks: Uint8Array[] = [];
    let size = 0;
    while (reader && size < MAX_BODY_BYTES) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      size += value.byteLength;
    }
    await reader?.cancel().catch(() => undefined);
    return { status: response.status, body: Buffer.concat(chunks).subarray(0, MAX_BODY_BYTES).toString('utf8') };
  }
}
