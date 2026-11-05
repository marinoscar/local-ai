// The model policy routes (docs/specs/gateway.md section 3.8). The list needs `gateway:use`; the write needs
// `gateway:admin`, the permission the Models page's edit controls are gated on.
import { Body, Controller, Get, Param, Put } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from '../../localai.permissions';
import { ModelIdParamSchema, parseWith, PutPolicyBodySchema, type ModelRowDto } from './policies.schemas';
import { PoliciesService } from './policies.service';

@ApiTags('localai-gateway-models')
@Controller('localai/gateway/models')
export class PoliciesController {
  constructor(private readonly policies: PoliciesService) {}

  @Get()
  @Auth({ permissions: [LOCALAI_PERMISSIONS.GATEWAY_USE.id] })
  @ApiOperation({ summary: 'Models with their policy and live state. Administrators also see disabled and unreachable models' })
  async list(@CurrentUser() user: RequestUser): Promise<{ models: ModelRowDto[] }> {
    return { models: await this.policies.list({ userId: user.id, permissions: user.permissions }) };
  }

  @Put(':modelId')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id] })
  @ApiOperation({ summary: 'Set a model policy: enabled, aliases, on-demand load, wait time, first-byte timeout, preferred nodes' })
  upsert(@Param('modelId') modelId: string, @Body() body: unknown, @CurrentUser() user: RequestUser): Promise<ModelRowDto> {
    return this.policies.upsert({ userId: user.id, permissions: user.permissions }, parseWith(ModelIdParamSchema, modelId), parseWith(PutPolicyBodySchema, body));
  }
}
