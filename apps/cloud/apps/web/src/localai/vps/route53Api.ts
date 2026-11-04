// The transport of the Route53 key page (docs/specs/cloud-app.md section 4.10). The key is WRITE-ONLY: the API never
// returns it, only whether one is stored and the credential store's masked hints.
import { api } from '../../api';

export interface Route53KeyStatus {
  configured: boolean;
  /** The masked hint of the access key id (never the key). */
  accessKeyHint: string | null;
  hostedZoneHint: string | null;
  updatedAt: string | null;
}

export interface Route53KeyInput {
  accessKeyId: string;
  secretAccessKey: string;
  hostedZoneId: string;
}

export interface Route53Client {
  status(): Promise<Route53KeyStatus>;
  save(input: Route53KeyInput): Promise<Route53KeyStatus>;
  remove(): Promise<Route53KeyStatus>;
}

const NO_STORE = { cache: 'no-store' } as const;

export const defaultRoute53Client: Route53Client = {
  status: () => api.get<Route53KeyStatus>('/localai/vps/route53', NO_STORE),
  save: (input) => api.put<Route53KeyStatus>('/localai/vps/route53', input, NO_STORE),
  remove: () => api.delete<Route53KeyStatus>('/localai/vps/route53', NO_STORE),
};
