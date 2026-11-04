// The catalog slice (docs/specs/cloud-app.md section 4.6): the UI routes, the node route, the two job handlers and
// the ports they run through. `PrismaService`, `JobsService`, `JobHandlerRegistry`, `AUDIT_SINK`, `EVENT_BUS` and
// `SystemSettingsService` are global. `NodeLinkModule` is imported for the binding guard behind `@NodeLinkAuth()`.
import { Module } from '@nestjs/common';

import { NodeCatalogController } from '../node-link/catalog.controller';
import { NodeLinkModule } from '../node-link/node-link.module';
import { CATALOG_ARCHIVE_FETCHER, GithubArchiveFetcher } from './archive-fetcher';
import { CatalogImportJob } from './catalog-import.job';
import { AictlLinter, CATALOG_LINTER } from './catalog-linter';
import { CatalogLintJob } from './catalog-lint.job';
import { CatalogRefreshQueue } from './catalog-refresh.queue';
import { CatalogSettings } from './catalog-settings';
import { CatalogController } from './catalog.controller';
import { CatalogService } from './catalog.service';

@Module({
  imports: [NodeLinkModule],
  controllers: [CatalogController, NodeCatalogController],
  providers: [
    CatalogService,
    CatalogSettings,
    CatalogRefreshQueue,
    CatalogImportJob,
    CatalogLintJob,
    { provide: CATALOG_ARCHIVE_FETCHER, useFactory: () => new GithubArchiveFetcher() },
    { provide: CATALOG_LINTER, useFactory: () => new AictlLinter() },
  ],
  exports: [CatalogService],
})
export class CatalogModule {}
