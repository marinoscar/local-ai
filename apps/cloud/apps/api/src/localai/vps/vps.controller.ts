// The routes of the VPS stack-agent slice (docs/specs/cloud-app.md sections 4.8 and 4.10). Every route needs `lab:admin`,
// the string the whole write side of the lab uses. Bodies are validated with Zod.
//
// The Route53 key is WRITE-ONLY: `PUT` stores it, `GET` answers whether one is stored with the credential store's masked
// hint, and nothing returns it. Responses of the node routes are `no-store`: they carry the run journal.
import { Body, Controller, Delete, Get, Header, HttpCode, NotFoundException, Param, Post, Put } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { parseWith } from '../catalog/catalog.schemas';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { NODE_ID_RE } from './vps-host';
import { Route53KeyBodySchema, Route53KeyService, type Route53KeyStatus } from './route53-key.service';
import { VpsService, type VpsStatusDto } from './vps.service';

const NO_STORE = 'no-store, max-age=0';
const LAB_ADMIN = LOCALAI_PERMISSIONS.LAB_ADMIN.id;

@ApiTags('localai-vps')
@Controller('localai')
export class VpsController {
  constructor(
    private readonly vps: VpsService,
    private readonly keys: Route53KeyService,
  ) {}

  @Get('nodes/:nodeId/publish')
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LAB_ADMIN] })
  @ApiOperation({ summary: 'Automatic publish: what blocks it, and the journal of the last run' })
  status(@Param('nodeId') nodeId: string): Promise<VpsStatusDto> {
    return this.vps.status(knownId(nodeId));
  }

  @Post('nodes/:nodeId/publish')
  @HttpCode(202)
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LAB_ADMIN] })
  @ApiOperation({ summary: 'Queue localai.vps.publish_node { nodeId } (DNS, certificate, vhost) for the VPS stack-agent' })
  publish(@Param('nodeId') nodeId: string, @CurrentUser() user: RequestUser): Promise<{ jobId: string }> {
    return this.vps.requestPublish(knownId(nodeId), user.id);
  }

  @Post('nodes/:nodeId/unpublish')
  @HttpCode(202)
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LAB_ADMIN] })
  @ApiOperation({ summary: 'Queue localai.vps.unpublish_node { nodeId }: remove the vhost and the A record (the certificate is left to expire)' })
  unpublish(@Param('nodeId') nodeId: string, @CurrentUser() user: RequestUser): Promise<{ jobId: string }> {
    return this.vps.requestUnpublish(knownId(nodeId), user.id);
  }

  @Get('vps/route53')
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LAB_ADMIN] })
  @ApiOperation({ summary: 'Whether the Route53 key is stored (masked hint only; the key is never returned)' })
  keyStatus(): Promise<Route53KeyStatus> {
    return this.keys.status();
  }

  @Put('vps/route53')
  @HttpCode(200)
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LAB_ADMIN] })
  @ApiOperation({ summary: 'Store (replace) the Route53 key of the VPS stack-agent. Write-only.' })
  storeKey(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<Route53KeyStatus> {
    return this.keys.set(parseWith(Route53KeyBodySchema, body), user.id);
  }

  @Delete('vps/route53')
  @HttpCode(200)
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LAB_ADMIN] })
  @ApiOperation({ summary: 'Remove the stored Route53 key' })
  clearKey(@CurrentUser() user: RequestUser): Promise<Route53KeyStatus> {
    return this.keys.clear(user.id);
  }
}

/** A node id outside the lab's shape cannot exist: answer 404 without a query. */
function knownId(nodeId: string): string {
  if (!NODE_ID_RE.test(nodeId)) throw new NotFoundException({ message: 'No Local AI node has this id', details: { reason: 'unknown_localai_node' } });
  return nodeId;
}
