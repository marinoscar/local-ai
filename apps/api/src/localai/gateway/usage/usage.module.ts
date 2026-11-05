// The gateway's limits and usage (GW-08): the recorder (the `UsageSink`), the rate limiter (the `GatewayLimits`), the
// ledger that connects them (the budget reads what the recorder queued), the usage API and the daily purge job.
// `GatewayProxyModule` binds `GATEWAY_USAGE_SINK` and `GATEWAY_LIMITS` to the two exports.
// `PrismaService`, `JobsService` and `JobHandlerRegistry` are global.
import { Module } from '@nestjs/common';

import { LocalaiSettingsModule } from '../../settings.module';
import { RateLimiter } from '../limits/rate-limiter';
import { UsageController } from './usage.controller';
import { UsageService } from './usage.service';
import { UsagePurgeHandler } from './usage-purge.job';
import { UsagePurgeTask } from './usage-purge.task';
import { UsageRecorder } from './usage-recorder';
import { DailyUsageLedger } from './usage-ledger';
import { GATEWAY_USAGE_STORE, PrismaUsageStore, type UsageStore } from './usage-store';

@Module({
  imports: [LocalaiSettingsModule],
  controllers: [UsageController],
  providers: [
    PrismaUsageStore,
    { provide: GATEWAY_USAGE_STORE, useExisting: PrismaUsageStore },
    { provide: DailyUsageLedger, inject: [PrismaUsageStore], useFactory: (reader: PrismaUsageStore) => new DailyUsageLedger(reader) },
    {
      provide: UsageRecorder,
      inject: [GATEWAY_USAGE_STORE, DailyUsageLedger],
      useFactory: (store: UsageStore, ledger: DailyUsageLedger) => new UsageRecorder({ store, ledger }),
    },
    { provide: RateLimiter, inject: [DailyUsageLedger], useFactory: (ledger: DailyUsageLedger) => new RateLimiter({ ledger }) },
    UsageService,
    UsagePurgeHandler,
    UsagePurgeTask,
  ],
  exports: [UsageRecorder, RateLimiter],
})
export class GatewayUsageModule {}
