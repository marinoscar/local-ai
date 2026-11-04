// Test helpers of the gateway specs: random tokens (never literals, so no key-shaped string sits in the source), fake
// ports for the authenticator, and a Fastify app with the gateway scope registered over those fakes.
import { randomBytes } from 'node:crypto';
import Fastify, { type FastifyInstance } from 'fastify';

import { GatewayAuthenticator, GATEWAY_USE_PERMISSION, type GatewayKeyRecord, type GatewayKeyStore, type OwnerAccess, type OwnerAccessReader } from './auth';
import type { GatewayConfig } from './gateway-settings';
import { registerGatewayRoutesOn, GATEWAY_PREFIX, type GatewayHandlers, type GatewayRouteDeps } from './routes';
import type { GatewayLogSink } from './request-log';
import { hashGatewayToken } from './token-verify';

/** A well-formed token: `lai_` + 43 base64url characters, fresh each call. */
export function makeToken(): string {
  return `lai_${randomBytes(32).toString('base64url')}`;
}

export function makeKey(token: string, overrides: Partial<GatewayKeyRecord> = {}): GatewayKeyRecord {
  return {
    id: randomBytes(8).toString('hex'),
    userId: 'user-a',
    name: 'Test key',
    kind: 'user',
    tokenHash: hashGatewayToken(token),
    tokenPrefix: token.slice(0, 10),
    allowedModels: [],
    rpmLimit: 60,
    concurrentLimit: 4,
    dailyTokenBudget: null,
    expiresAt: new Date(Date.now() + 86_400_000),
    revokedAt: null,
    ...overrides,
  };
}

export class FakeKeyStore implements GatewayKeyStore {
  readonly keys = new Map<string, GatewayKeyRecord>();
  lookups = 0;
  touches: Array<{ keyId: string; at: Date }> = [];
  touchError: Error | null = null;

  add(key: GatewayKeyRecord): GatewayKeyRecord {
    this.keys.set(key.tokenHash, key);
    return key;
  }

  async findByTokenHash(tokenHash: string): Promise<GatewayKeyRecord | null> {
    this.lookups += 1;
    return this.keys.get(tokenHash) ?? null;
  }

  async touchLastUsed(keyId: string, at: Date): Promise<void> {
    this.touches.push({ keyId, at });
    if (this.touchError) throw this.touchError;
  }
}

export class FakeOwners implements OwnerAccessReader {
  readonly owners = new Map<string, OwnerAccess | null>();
  reads = 0;

  set(userId: string, access: OwnerAccess | null): void {
    this.owners.set(userId, access);
  }

  async read(userId: string): Promise<OwnerAccess | null> {
    this.reads += 1;
    return this.owners.get(userId) ?? null;
  }
}

export const ACTIVE_OWNER: OwnerAccess = { active: true, permissions: [GATEWAY_USE_PERMISSION] };

export interface CapturedLogs {
  info: string[];
  warn: string[];
  error: Array<{ message: string; stack?: string }>;
}

export function captureSink(): { sink: GatewayLogSink; logs: CapturedLogs } {
  const logs: CapturedLogs = { info: [], warn: [], error: [] };
  const sink: GatewayLogSink = {
    info: (line) => logs.info.push(line),
    warn: (line) => logs.warn.push(line),
    error: (message, stack) => logs.error.push({ message, stack }),
  };
  return { sink, logs };
}

export interface TestGateway {
  app: FastifyInstance;
  store: FakeKeyStore;
  owners: FakeOwners;
  authenticator: GatewayAuthenticator;
  logs: CapturedLogs;
  /** Fastify's own log output (every line, as text), when `fastifyLogs` was requested. */
  fastifyLog: string[];
  token: string;
  prefix: string;
  maintenance: { active: boolean };
  config: { enabled: boolean; origins: string[] };
  routes: Array<{ method: string; url: string }>;
}

export interface BuildOptions {
  handlers?: Partial<GatewayHandlers>;
  fastifyLogs?: boolean;
  authenticator?: GatewayRouteDeps['authenticator'];
  /** Runs on the root instance before it is ready (a spec registers a rogue route here). */
  beforeReady?: (app: FastifyInstance) => void;
}

/** A Fastify instance with the gateway registered over fakes, one active owner and one valid key (`token`). */
export async function buildGateway(options: BuildOptions = {}): Promise<TestGateway> {
  const fastifyLog: string[] = [];
  const app = options.fastifyLogs
    ? Fastify({ logger: { level: 'trace', stream: { write: (chunk: string) => void fastifyLog.push(chunk) } } })
    : Fastify();
  const store = new FakeKeyStore();
  const owners = new FakeOwners();
  const token = makeToken();
  store.add(makeKey(token));
  owners.set('user-a', ACTIVE_OWNER);
  const authenticator = new GatewayAuthenticator({ store, owners });
  const { sink, logs } = captureSink();
  const maintenance = { active: false };
  const config = { enabled: true, origins: [] as string[] };
  const gatewayConfig: GatewayConfig = { enabled: () => config.enabled, corsOrigins: async () => config.origins };
  const routes: Array<{ method: string; url: string }> = [];
  app.addHook('onRoute', (route) => {
    for (const method of Array.isArray(route.method) ? route.method : [route.method]) routes.push({ method, url: route.url });
  });
  await registerGatewayRoutesOn(app, {
    authenticator: options.authenticator ?? authenticator,
    config: gatewayConfig,
    maintenance: { isActive: async () => maintenance.active },
    logger: sink,
    handlers: options.handlers,
  });
  options.beforeReady?.(app);
  await app.ready();
  return { app, store, owners, authenticator, logs, fastifyLog, token, prefix: GATEWAY_PREFIX, maintenance, config, routes };
}
