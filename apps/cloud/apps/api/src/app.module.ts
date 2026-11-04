import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { APP_PIPE } from '@nestjs/core';
import { EventEmitterModule } from '@nestjs/event-emitter';
import { ScheduleModule } from '@nestjs/schedule';
import { ZodValidationPipe } from 'nestjs-zod';
import { RegistryFreezeService } from '@marinoscar/platform-api/core';

import configuration from './config/configuration';
import { LocalaiModule } from './localai/localai.module';
import { PLATFORM_MODULES } from './platform/platform';
import { PrismaModule } from './prisma/prisma.service';

/**
 * The root module: framework modules, the platform (src/platform/platform.ts)
 * and the app's own feature modules. Never edit platform behaviour here;
 * configure it through the slices' `forRoot()` options and host ports.
 */
@Module({
  imports: [
    ConfigModule.forRoot({ isGlobal: true, load: [configuration] }),
    ScheduleModule.forRoot(),
    EventEmitterModule.forRoot(),
    PrismaModule,
    ...PLATFORM_MODULES,
    // The app's features go here.
    LocalaiModule,
  ],
  providers: [
    { provide: APP_PIPE, useClass: ZodValidationPipe },
    // No APP_GUARD, APP_FILTER or APP_INTERCEPTOR here: the host core
    // (PlatformHostCoreModule) registers the maintenance guard (the only
    // global guard; a route without @Auth() is public), the exception filter
    // and the `{ data }` envelope.
    // Freezes every platform registry once the graph is built.
    RegistryFreezeService,
  ],
})
export class AppModule {}
