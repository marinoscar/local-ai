// The catalog routes of the cloud UI (docs/specs/cloud-app.md section 3.9). Reads need `lab:read`; everything that
// changes anything needs `lab:admin`. Bodies and queries are validated with Zod (`catalog.schemas.ts`).
import { Body, Controller, Get, HttpCode, Param, Post, Query } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { ApproveBodySchema, CreateVersionBodySchema, GetVersionQuerySchema, ImportBodySchema, parseWith } from './catalog.schemas';
import { CatalogService } from './catalog.service';

@ApiTags('localai-catalog')
@Controller('localai/catalog')
export class CatalogController {
  constructor(private readonly catalog: CatalogService) {}

  @Get('versions')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'List the catalog versions, newest first (without file contents)' })
  list() {
    return this.catalog.list();
  }

  @Get('versions/:version')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'One version: files, lint result and, with ?diff=approved, the file-level difference to the approved one' })
  get(@Param('version') version: string, @Query() query: unknown) {
    return this.catalog.get(version, parseWith(GetVersionQuerySchema, query));
  }

  @Post('import')
  @HttpCode(202)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Import a catalog-<version> release tag from GitHub (queues the download; the draft appears when it finishes)' })
  import(@Body() body: unknown, @CurrentUser() user: RequestUser) {
    return this.catalog.requestImport(parseWith(ImportBodySchema, body).tag, user.id);
  }

  @Post('versions')
  @HttpCode(201)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Save an edited or uploaded catalog as a draft (queues its lint)' })
  create(@Body() body: unknown, @CurrentUser() user: RequestUser) {
    return this.catalog.createDraft(parseWith(CreateVersionBodySchema, body), user.id);
  }

  @Post('versions/:version/lint')
  @HttpCode(202)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Run the catalog lint again' })
  lint(@Param('version') version: string, @CurrentUser() user: RequestUser) {
    return this.catalog.requestLint(version, user.id);
  }

  @Post('versions/:version/approve')
  @HttpCode(200)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Approve a valid version (the previous approved one is superseded); optionally ask the nodes to refresh' })
  approve(@Param('version') version: string, @Body() body: unknown, @CurrentUser() user: RequestUser) {
    return this.catalog.approve(version, parseWith(ApproveBodySchema, body), user.id);
  }
}
