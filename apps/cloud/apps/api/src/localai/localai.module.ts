// The Local AI feature module (docs/specs/cloud-app.md): the app's own code on top of the platform. Each slice of the
// cloud adds its module here as it lands (node link now; state, commands, catalog, gateway later).
import { Module } from '@nestjs/common';

import { AlertsModule } from './alerts/alerts.module';
import { LocalaiAuditModule } from './audit/audit.module';
import { CatalogModule } from './catalog/catalog.module';
import { CommandsModule } from './commands/commands.module';
import { LocalaiDoctorModule } from './doctor';
import { LabNodesModule } from './nodes/lab-nodes.module';
import { LocalaiNodesModule } from './nodes/nodes.module';
import { NodeLinkModule } from './node-link/node-link.module';
import { VpsModule } from './vps/vps.module';
import { WizardModule } from './wizard/wizard.module';

@Module({ imports: [NodeLinkModule, LocalaiNodesModule, CatalogModule, CommandsModule, LabNodesModule, LocalaiDoctorModule, WizardModule, AlertsModule, VpsModule, LocalaiAuditModule] })
export class LocalaiModule {}
