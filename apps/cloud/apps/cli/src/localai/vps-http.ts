// The final validation (docs/specs/cloud-app.md section 4.10 step 6), behind a port: the status code of an https GET.
// https only, redirects never followed, the body never read, 10 s limit.
import { VPS_HOST_RE, VpsInputError } from './vps-host.js';

export interface HttpProbe {
  /** The status code of `GET https://<host><path>` without credentials; 0 when the host cannot be reached. */
  status(host: string, path: '/health' | '/v1/models'): Promise<number>;
}

export class FetchHttpProbe implements HttpProbe {
  constructor(private readonly fetchImpl: typeof globalThis.fetch = globalThis.fetch) {}

  async status(host: string, path: '/health' | '/v1/models'): Promise<number> {
    if (!VPS_HOST_RE.test(host)) throw new VpsInputError('The host name is not one this agent may publish.');
    try {
      const response = await this.fetchImpl(`https://${host}${path}`, { method: 'GET', redirect: 'manual', signal: AbortSignal.timeout(10_000) });
      await response.body?.cancel();
      return response.status;
    } catch {
      return 0;
    }
  }
}
