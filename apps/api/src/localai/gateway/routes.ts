// The raw Fastify routes of the OpenAI-compatible gateway (docs/specs/gateway.md section 3.1). They live under
// `/api/gateway/v1/*` (the inner nginx maps the public `/v1/`), are registered by `registerGatewayRoutes(app)` from
// `createApp()` in src/main.ts after `NestFactory.create` and before `listen`, and run NONE of Nest's request machinery:
// no TransformInterceptor, HttpExceptionFilter, JwtAuthGuard, middleware or MaintenanceGuard (they would rewrite the
// OpenAI shapes or read `Bearer lai_...` as a JWT). Everything the pipeline needs is therefore here:
//
//   request id -> CORS headers -> gateway enabled / maintenance? -> CORS preflight ends -> authenticate
//   -> (handler) body limit + parse -> ... later items: model, scope, limits, selection, attempts.
//
// The pipeline is ONE `onRequest` hook of the encapsulated scope, so it runs before Fastify reads a body (an
// unauthenticated client never makes the gateway buffer 8 MiB), and every route of the scope, including a route a later
// item adds, goes through it. A route is authenticated unless it is declared `public` (only `GET /v1/`); the
// `gateway-routes.spec.ts` suite enumerates the registered routes and fails on one that answers without a key.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { Logger } from '@nestjs/common';
import { MaintenanceModeService } from '@marinoscar/platform-api/host';
import type { FastifyInstance, FastifyReply, FastifyRequest, HTTPMethods } from 'fastify';

import { clientIp, GatewayAuthenticator, type GatewayPrincipal } from './auth';
import { GATEWAY_AUDIO_BODY_LIMIT, GATEWAY_JSON_BODY_LIMIT, bodyErrorToGatewayError, parseInferenceBody } from './body';
import { corsHeaders } from './cors';
import { GatewayError, sendGatewayError } from './errors';
import { GatewaySettingsReader, type GatewayConfig } from './gateway-settings';
import { buildGatewayIndex } from './index-route';
import { resolveRequestId } from './request-id';
import { formatRequestLog, newRequestLog, type GatewayLogSink, type GatewayRequestLog } from './request-log';

/** Where the scope is mounted; the public path is `/v1/...`. */
export const GATEWAY_PREFIX = '/api/gateway/v1';

/** What a handler receives: the request, its reply and everything the pipeline already established. */
export interface GatewayRequestContext {
  request: FastifyRequest;
  reply: FastifyReply;
  requestId: string;
  /** Set for every authenticated route. */
  principal: GatewayPrincipal;
  /** The per-request log record; a handler fills the fields it learns (model, node, tokens, ...). */
  log: GatewayRequestLog;
  /** The JSON body parsed and checked by `parseInferenceBody()` (inference routes only). */
  body: { model: string; body: Record<string, unknown> } | null;
}

export type GatewayHandler = (ctx: GatewayRequestContext) => Promise<unknown>;

export interface GatewayHandlers {
  listModels: GatewayHandler;
  getModel: GatewayHandler;
  chatCompletions: GatewayHandler;
  completions: GatewayHandler;
  embeddings: GatewayHandler;
  transcriptions: GatewayHandler;
}

/** The inference endpoints are stubs until the models item and the proxying items replace them. */
const notBuiltYet: GatewayHandler = async () => {
  throw new GatewayError('LAI-GW-051', { message: 'This gateway endpoint is not available yet.' });
};

export const DEFAULT_GATEWAY_HANDLERS: GatewayHandlers = {
  listModels: notBuiltYet,
  getModel: notBuiltYet,
  chatCompletions: notBuiltYet,
  completions: notBuiltYet,
  embeddings: notBuiltYet,
  transcriptions: notBuiltYet,
};

export interface GatewayRouteDeps {
  authenticator: Pick<GatewayAuthenticator, 'authenticate'>;
  config: GatewayConfig;
  maintenance: { isActive(): Promise<boolean> };
  logger: GatewayLogSink;
  handlers?: Partial<GatewayHandlers>;
}

interface RouteConfig {
  gatewayPublic?: boolean;
  gatewayEndpoint?: string;
}

/** Fastify request logging for these routes: warnings and above only, and a serializer that cannot see headers or a body. */
const ROUTE_LOGGING = {
  logLevel: 'warn' as const,
  logSerializers: {
    req: (req: { method?: string; url?: string }) => `${req.method ?? ''} ${(req.url ?? '').split('?')[0]}`,
    res: (res: { statusCode?: number }) => String(res.statusCode ?? ''),
  },
};

const contexts = new WeakMap<FastifyRequest, GatewayRequestContext>();

function routeConfig(request: FastifyRequest): RouteConfig {
  return (request.routeOptions?.config ?? {}) as RouteConfig;
}

function contextOf(request: FastifyRequest): GatewayRequestContext {
  const ctx = contexts.get(request);
  if (!ctx) throw new GatewayError('LAI-GW-052');
  return ctx;
}

/** The Nest logger as a sink: one JSON line per request. */
export function nestLogSink(logger: Logger = new Logger('Gateway')): GatewayLogSink {
  return {
    info: (line) => logger.log(line),
    warn: (line) => logger.warn(line),
    error: (message, stack) => logger.error(message, stack),
  };
}

/**
 * Registers the gateway scope on a Fastify instance. Split from `registerGatewayRoutes` so a spec can supply fakes
 * for the services without a Nest application.
 */
export async function registerGatewayRoutesOn(instance: FastifyInstance, deps: GatewayRouteDeps): Promise<void> {
  const handlers: GatewayHandlers = { ...DEFAULT_GATEWAY_HANDLERS, ...deps.handlers };

  await instance.register(
    async (scope) => {
      // Bodies of other content types (multipart for transcriptions, an unknown endpoint's payload) are not read here:
      // the route that wants them reads the stream itself; Fastify would otherwise answer 415 before the 404.
      scope.addContentTypeParser('*', (_request, _payload, done) => done(null));

      scope.setErrorHandler((error, request, reply) => {
        if (typeof reply.getHeader('x-request-id') !== 'string') void reply.header('x-request-id', resolveRequestId(request.headers['x-request-id']));
        const ctx = contexts.get(request);
        let gatewayError: GatewayError;
        if (error instanceof GatewayError) {
          gatewayError = error;
        } else {
          const bodyError = bodyErrorToGatewayError(error);
          if (bodyError) {
            gatewayError = bodyError;
          } else {
            gatewayError = new GatewayError('LAI-GW-052');
            const reason = error instanceof Error ? error : new Error('non-error thrown');
            deps.logger.error(`unexpected gateway error (request ${String(reply.getHeader('x-request-id'))}): ${reason.name}`, reason.stack);
          }
        }
        if (ctx) ctx.log.error_code = gatewayError.localaiCode;
        return sendGatewayError(reply, gatewayError);
      });

      scope.addHook('onRequest', async (request, reply) => {
        const requestId = resolveRequestId(request.headers['x-request-id']);
        void reply.header('x-request-id', requestId);
        const log = newRequestLog(requestId);
        log.endpoint = routeConfig(request).gatewayEndpoint ?? null;
        const ctx: GatewayRequestContext = { request, reply, requestId, principal: undefined as unknown as GatewayPrincipal, log, body: null };
        contexts.set(request, ctx);

        // CORS headers go on every response of an allowed origin, errors included, so a browser can read them.
        const preflight = request.method === 'OPTIONS';
        const origins = await deps.config.corsOrigins().catch(() => [] as readonly string[]);
        for (const [name, value] of Object.entries(corsHeaders(request.headers.origin, origins, preflight))) void reply.header(name, value);

        // A maintenance lookup that fails must not take the gateway down with it: treat it as "not in maintenance".
        const inMaintenance = await deps.maintenance.isActive().catch((error: unknown) => {
          deps.logger.warn(`could not read the maintenance state, assuming off: ${error instanceof Error ? error.name : 'unknown error'}`);
          return false;
        });
        if (!deps.config.enabled() || inMaintenance) throw new GatewayError('LAI-GW-051');
        if (preflight) return reply.code(204).send();
        if (routeConfig(request).gatewayPublic) return undefined;

        ctx.principal = await deps.authenticator.authenticate({ authorization: request.headers.authorization, ip: clientIp(request.headers, request.ip) });
        log.key_prefix = ctx.principal.key.tokenPrefix;
        log.user_id = ctx.principal.userId;
        return undefined;
      });

      scope.addHook('onResponse', async (request, reply) => {
        const ctx = contexts.get(request);
        if (!ctx) return;
        ctx.log.status ??= reply.statusCode;
        ctx.log.duration_ms ??= Math.round(reply.elapsedTime);
        const line = formatRequestLog(ctx.log);
        if (reply.statusCode >= 500) deps.logger.warn(line);
        else deps.logger.info(line);
      });

      /** Declares one route. Authenticated unless `public`; the handler receives the context. */
      const define = (
        method: HTTPMethods | HTTPMethods[],
        url: string,
        options: { endpoint: string; public?: boolean; bodyLimit?: number; inferenceBody?: boolean },
        handler: GatewayHandler,
      ): void => {
        scope.route({
          method,
          url,
          bodyLimit: options.bodyLimit ?? GATEWAY_JSON_BODY_LIMIT,
          config: { gatewayPublic: options.public === true, gatewayEndpoint: options.endpoint } satisfies RouteConfig,
          ...ROUTE_LOGGING,
          handler: async (request, reply) => {
            const ctx = contextOf(request);
            if (options.inferenceBody) {
              ctx.body = parseInferenceBody(request.body);
              ctx.log.requested_model = ctx.body.model;
            }
            const result = await handler({ ...ctx, reply });
            return result === undefined ? reply : result;
          },
        });
      };

      define('GET', '/', { endpoint: 'index', public: true }, async ({ reply }) => reply.code(200).send(buildGatewayIndex()));
      define('GET', '/models', { endpoint: 'models' }, handlers.listModels);
      define('GET', '/models/:id', { endpoint: 'models' }, handlers.getModel);
      define('POST', '/chat/completions', { endpoint: 'chat.completions', inferenceBody: true }, handlers.chatCompletions);
      define('POST', '/completions', { endpoint: 'completions', inferenceBody: true }, handlers.completions);
      define('POST', '/embeddings', { endpoint: 'embeddings', inferenceBody: true }, handlers.embeddings);
      define('POST', '/audio/transcriptions', { endpoint: 'audio.transcriptions', bodyLimit: GATEWAY_AUDIO_BODY_LIMIT }, handlers.transcriptions);
      // Preflight: answered by the pipeline hook (it ends there); the route exists so Fastify routes the method.
      define('OPTIONS', '/*', { endpoint: 'preflight' }, async ({ reply }) => reply.code(204).send());
      // Anything else under /v1/ is not served. It authenticates first, like every route, so an anonymous probe learns nothing.
      define(['GET', 'POST', 'PUT', 'PATCH', 'DELETE'], '/*', { endpoint: 'unsupported' }, async () => {
        throw new GatewayError('LAI-GW-024');
      });
    },
    { prefix: GATEWAY_PREFIX },
  );
}

/** Registers the gateway on the Nest application's Fastify instance, resolving the services from the Nest container. */
export async function registerGatewayRoutes(app: NestFastifyApplication): Promise<void> {
  const maintenance = app.get(MaintenanceModeService, { strict: false });
  await registerGatewayRoutesOn(app.getHttpAdapter().getInstance() as unknown as FastifyInstance, {
    authenticator: app.get(GatewayAuthenticator, { strict: false }),
    config: app.get(GatewaySettingsReader, { strict: false }),
    // The platform names the question `isEnabled()`: whether traffic is being held back at all, whoever asks.
    maintenance: { isActive: () => maintenance.isEnabled() },
    logger: nestLogSink(),
  });
}
