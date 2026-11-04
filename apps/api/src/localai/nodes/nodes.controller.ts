// The UI-facing node routes under `/api/localai/nodes` (docs/specs/cloud-app.md section 3.9). CLOUD-07 adds the
// "Ask all nodes to re-send keys" action; the list, detail, create and retire routes are CLOUD-09 and CLOUD-14, which
// append to this controller.
import { Controller, HttpCode, Post } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser } from '@marinoscar/platform-api/identity';

import { ResendKeysService } from '../keys/resend-keys.service';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';

@ApiTags('localai-nodes')
@Controller('localai/nodes')
export class NodesController {
  constructor(private readonly resendKeys: ResendKeysService) {}

  @Post('resend-keys')
  @HttpCode(200)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Ask every enrolled node to re-send its llama key (queues status.refresh on each)' })
  async resendKeysToAll(@CurrentUser('id') actorUserId: string): Promise<{ commandIds: string[] }> {
    return { commandIds: await this.resendKeys.resendAll(actorUserId ?? null) };
  }
}
