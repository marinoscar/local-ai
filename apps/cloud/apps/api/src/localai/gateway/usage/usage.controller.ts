// `GET /api/localai/gateway/usage` (docs/specs/gateway.md section 3.8). Needs `gateway:use`; `?all=1` (every key) needs
// `gateway:admin`, which the service checks against the caller's permissions. Read-only.
import { Controller, Get, Query } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from '../../localai.permissions';
import { parseWith } from '../keys/keys.schemas';
import { UsageQuerySchema, type UsageDto } from './usage.schemas';
import { UsageService } from './usage.service';

@ApiTags('localai-gateway-usage')
@Controller('localai/gateway/usage')
export class UsageController {
  constructor(private readonly usage: UsageService) {}

  @Get()
  @Auth({ permissions: [LOCALAI_PERMISSIONS.GATEWAY_USE.id] })
  @ApiOperation({
    summary:
      'Gateway usage of your keys (?all=1: every key, gateway:admin). groupBy=day reads the daily table (default range: the last 30 UTC days); groupBy=model|node reads the per-request rows and needs a range of at most 7 days',
  })
  query(@Query() query: unknown, @CurrentUser() user: RequestUser): Promise<UsageDto> {
    return this.usage.query({ userId: user.id, permissions: user.permissions }, parseWith(UsageQuerySchema, query));
  }
}
