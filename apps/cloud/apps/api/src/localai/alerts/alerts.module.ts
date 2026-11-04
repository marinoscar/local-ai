// The alert engine (docs/specs/cloud-app.md section 4.9): the rules' service, the queue job, the 5-minute schedule that
// only enqueues it, the two routes and the notifier. `PrismaService`, `JobsService`, the job registry, the event bus and the
// audit sink are global; the notifications dispatcher is global when the notifications slice is mounted.
import { Module } from '@nestjs/common';

import { LOCALAI_CLOCK, systemLocalaiClock } from '../commands/command-events';
import { LabNodesModule } from '../nodes/lab-nodes.module';
import { LOCALAI_ALERT_NOTIFIER } from './alert-ports';
import { AlertSettingsReader } from './alert-settings';
import { ALERT_STORE } from './alert-store';
import { AlertsEvaluateHandler } from './alerts-evaluate.job';
import { AlertsController } from './alerts.controller';
import { AlertsEvaluateTask } from './alerts.cron';
import { AlertsService } from './alerts.service';
import { NotificationsAlertNotifier } from './alerts-notifier';
import { PrismaAlertStore } from './prisma-alert.store';

@Module({
  imports: [LabNodesModule],
  controllers: [AlertsController],
  providers: [
    AlertsService,
    AlertSettingsReader,
    AlertsEvaluateHandler,
    AlertsEvaluateTask,
    PrismaAlertStore,
    { provide: ALERT_STORE, useExisting: PrismaAlertStore },
    { provide: LOCALAI_ALERT_NOTIFIER, useClass: NotificationsAlertNotifier },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  // `raiseExternal` / `clearExternal` are the gateway probe's entry (GW-04).
  exports: [AlertsService],
})
export class AlertsModule {}
