// The OpenTelemetry SDK must be installed before anything that loads Nest (src/instrumentation.ts).
import { sdk } from './instrumentation';
import 'reflect-metadata';

import fastifyCookie from '@fastify/cookie';
import multipart from '@fastify/multipart';
import { Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { NestFactory } from '@nestjs/core';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { APP_NAME, REPO_URL } from '@app/shared';
import { verifyEncryptionKeyAtStartup } from '@marinoscar/platform-api/core';
import { registerRequestSpanAttributes } from '@marinoscar/platform-api/otel-core';
import { registerPlatformDocs, resolveApiVersion, type PlatformOpenApiOptions } from '@marinoscar/platform-api/host';
import { verifyTenancyModeAtStartup } from '@marinoscar/platform-api/identity';
import { STORAGE_OPTIONS, simpleUploadFileSizeLimit, type ResolvedStorageModuleOptions } from '@marinoscar/platform-api/storage';

import { AppModule } from './app.module';
import { isSliceEnabled } from './platform/slices/manifest';
import { PrismaService } from './prisma/prisma.service';

/** The app's identity in the OpenAPI document and on `/api/docs`. */
export const APP_OPENAPI: PlatformOpenApiOptions = {
  appName: APP_NAME,
  repoUrl: REPO_URL,
  version: () => resolveApiVersion(__dirname),
};

/**
 * Builds the application: Fastify, the cookie plugin, the multipart plugin when
 * the storage slice is on, the `/api` prefix, and
 * the API reference (`/api/docs`, `/api/openapi.json`; a generation failure
 * degrades both to a 503, never the boot). Shared by the boot and the db-tier tests.
 */
export async function createApp(): Promise<NestFastifyApplication> {
  const app = await NestFactory.create<NestFastifyApplication>(AppModule, new FastifyAdapter(), { bufferLogs: false });
  // Route and caller attributes on the HTTP server span (`http.route`, `app.route.matched`),
  // first so it runs ahead of every other hook. Only when the SDK is installed.
  registerRequestSpanAttributes(app.getHttpAdapter().getInstance(), sdk !== null);
  await app.register(fastifyCookie, { secret: process.env.COOKIE_SECRET || process.env.JWT_SECRET });
  if (isSliceEnabled('storage')) {
    // The simple (single request) upload route reads multipart bodies; its size
    // ceiling is the smaller of the deployment's limit and the slice's own.
    const storageOptions = app.get<ResolvedStorageModuleOptions>(STORAGE_OPTIONS, { strict: false });
    const maxFileSize = app.get(ConfigService).get<number>('storage.maxFileSize');
    await app.register(multipart, {
      limits: { fileSize: simpleUploadFileSizeLimit(maxFileSize, storageOptions.maxSimpleUploadBytes), files: 1 },
    });
  }
  app.setGlobalPrefix('api');
  registerPlatformDocs(app, APP_OPENAPI);
  return app;
}

async function bootstrap(): Promise<void> {
  const logger = new Logger('Bootstrap');
  if (process.env.NODE_ENV === 'production' && process.env.TEST_AUTH_ENABLED === 'true') {
    throw new Error('TEST_AUTH_ENABLED must not be true in production');
  }
  // Fail fast, before a port is bound: a bad TENANCY_MODE, or stored
  // credentials without the SECRETS_ENCRYPTION_KEY that decrypts them.
  verifyTenancyModeAtStartup(process.env, logger);
  const app = await createApp();
  await verifyEncryptionKeyAtStartup(() => app.get(PrismaService).credential.count(), logger);
  app.enableShutdownHooks();
  const port = parseInt(process.env.PORT || '3000', 10);
  await app.listen(port, '0.0.0.0');
  logger.log(`API listening on :${port}/api`);
}

if (require.main === module) {
  void bootstrap();
}
