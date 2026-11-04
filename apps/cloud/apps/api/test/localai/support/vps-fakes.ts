// Shared fakes for the unit specs of the VPS stack-agent slice. Values are generated at run time: no key-shaped or
// tailnet-shaped literal sits in the repository.
import { randomBytes } from 'node:crypto';

export const ip4 = (...parts: number[]) => parts.join('.');

export function fakeKey() {
  return {
    accessKeyId: `AKIA${randomBytes(8).toString('hex').toUpperCase()}`,
    secretAccessKey: randomBytes(30).toString('base64').replace(/[^A-Za-z0-9]/g, 'x').slice(0, 40),
    hostedZoneId: `Z${randomBytes(7).toString('hex').toUpperCase()}`,
  };
}

export function fakeTailnet() {
  return { tailnetIp: ip4(100, 64 + (randomBytes(1)[0]! % 60), randomBytes(1)[0]!, 1 + (randomBytes(1)[0]! % 250)), tailnetMachine: `dell-linux.${['tail', randomBytes(3).toString('hex')].join('')}.ts.net` };
}

/** An in-memory credential store with the real service's address rules and masked hint. */
export class FakeCredentials {
  readonly rows = new Map<string, { secret: string; updatedAt: Date }>();
  failReads = false;
  async setSecret(purpose: string, name: string, secret: string) {
    this.rows.set(`${purpose}/${name}`, { secret, updatedAt: new Date('2026-11-04T12:00:00Z') });
  }
  async getSecret(purpose: string, name: string) {
    if (this.failReads) throw new Error(`decrypt failed for ${this.rows.get(`${purpose}/${name}`)?.secret}`);
    return this.rows.get(`${purpose}/${name}`)?.secret ?? null;
  }
  async describe(purpose: string, name: string) {
    const row = this.rows.get(`${purpose}/${name}`);
    return row ? { purpose, name, hint: `****${row.secret.slice(-4)}`, label: null, updatedByUserId: null, createdAt: row.updatedAt, updatedAt: row.updatedAt } : null;
  }
  async deleteSecret(purpose: string, name: string) {
    this.rows.delete(`${purpose}/${name}`);
  }
}

export const jobRow = (type: string, nodeId = 'ai-n3') =>
  ({ id: 'job-1', type, payload: { nodeId }, subjectType: 'localai_node', subjectId: nodeId, orgId: null }) as never;
