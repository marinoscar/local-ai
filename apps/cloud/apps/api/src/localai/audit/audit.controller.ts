// The audit trail for the lab owner (docs/specs/cloud-app.md section 3.9): read-only, `lab:admin` only. A holder of
// `lab:read` or `lab:operate` alone (org_admin, contributor, viewer) is answered 403 by the guard before this runs.
import { Controller, Get, Query } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth } from '@marinoscar/platform-api/identity';

import { parseWith } from '../commands/command-api.schemas';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { AuditQuerySchema, type AuditFiltersDto, type AuditPageDto } from './audit.schemas';
import { AuditService } from './audit.service';

@ApiTags('localai-audit')
@Controller('localai/audit')
export class AuditController {
  constructor(private readonly audit: AuditService) {}

  @Get()
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({
    summary: 'The Local AI audit trail: audit_events whose action starts with localai., newest first, 50 per page (?nodeId=&action=&from=&to=&page=). from defaults to 30 days ago',
  })
  list(@Query() query: unknown): Promise<AuditPageDto> {
    return this.audit.list(parseWith(AuditQuerySchema, query));
  }

  @Get('filters')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'What the audit page filters offer: the known actions by group and every node' })
  filters(): Promise<AuditFiltersDto> {
    return this.audit.filters();
  }
}
