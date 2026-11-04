// The gateway's four Doctor checks (docs/specs/gateway.md section 7), registered with the platform's registry next to the
// cloud's seven (`../../doctor`), so all eleven appear on `/admin/settings/doctor`. The checks read the routing table,
// the settings reader, the maintenance service and the usage recorder through narrow ports.
import { Module } from '@nestjs/common';
import { DoctorCheckRegistry } from '@marinoscar/platform-api/doctor';
import { MaintenanceModeService } from '@marinoscar/platform-api/host';

import { GatewayCoreModule } from '../gateway-core.module';
import { GatewaySettingsReader } from '../gateway-settings';
import { GatewayRoutingModule } from '../routing/routing.module';
import { RoutingTable } from '../routing/routing-table';
import { GatewayUsageModule } from '../usage/usage.module';
import { UsageRecorder } from '../usage/usage-recorder';
import { GatewayEnabledDoctorCheck } from './gateway-enabled.check';
import { GatewayKeysDoctorCheck } from './gateway-keys.check';
import { GatewayRoutableDoctorCheck } from './gateway-routable.check';
import { GatewayUsageDoctorCheck } from './gateway-usage.check';

/** The ids of the four checks, in report order. */
export const GATEWAY_DOCTOR_CHECK_IDS = ['localai.gateway.enabled', 'localai.gateway.routable', 'localai.gateway.keys', 'localai.gateway.usage'] as const;

@Module({
  imports: [GatewayCoreModule, GatewayRoutingModule, GatewayUsageModule],
  providers: [
    {
      provide: GatewayEnabledDoctorCheck,
      inject: [DoctorCheckRegistry, GatewaySettingsReader, MaintenanceModeService],
      useFactory: (registry: DoctorCheckRegistry, settings: GatewaySettingsReader, maintenance: MaintenanceModeService) => new GatewayEnabledDoctorCheck(registry, settings, maintenance),
    },
    {
      provide: GatewayRoutableDoctorCheck,
      inject: [DoctorCheckRegistry, RoutingTable],
      useFactory: (registry: DoctorCheckRegistry, table: RoutingTable) => new GatewayRoutableDoctorCheck(registry, table),
    },
    {
      provide: GatewayKeysDoctorCheck,
      inject: [DoctorCheckRegistry, RoutingTable],
      useFactory: (registry: DoctorCheckRegistry, table: RoutingTable) => new GatewayKeysDoctorCheck(registry, table),
    },
    {
      provide: GatewayUsageDoctorCheck,
      inject: [DoctorCheckRegistry, UsageRecorder],
      useFactory: (registry: DoctorCheckRegistry, recorder: UsageRecorder) => new GatewayUsageDoctorCheck(registry, recorder),
    },
  ],
})
export class GatewayDoctorModule {}
