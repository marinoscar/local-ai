// The key routes (docs/specs/gateway.md section 3.8). Every route needs `gateway:use`; what more a request needs
// (`?all=1`, a service key, another owner, limits above the default, widening) is decided in `key-rules.ts` against the
// caller's permissions, so a viewer is refused by the guard before anything is read.
//
// The creation response is the only place a raw token is ever sent. Neither the platform's logging interceptor (one
// `METHOD url - Nms` line) nor this app's Fastify instance (request logging off) writes request or response bodies.
import { Body, Controller, Delete, Get, HttpCode, Param, Patch, Post, Query } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from '../../localai.permissions';
import { KeyService } from './key.service';
import { CreateKeyBodySchema, KeyIdSchema, ListKeysQuerySchema, PatchKeyBodySchema, parseWith, type CreatedKeyDto, type KeyDto } from './keys.schemas';

const USE = [LOCALAI_PERMISSIONS.GATEWAY_USE.id];

@ApiTags('localai-gateway-keys')
@Controller('localai/gateway/keys')
export class KeysController {
  constructor(private readonly keys: KeyService) {}

  @Get()
  @Auth({ permissions: USE })
  @ApiOperation({ summary: 'Your gateway keys (prefix, name, models, limits, expiry, last used). ?all=1 lists every key (gateway:admin); ?includeRevoked=1 adds revoked ones' })
  async list(@Query() query: unknown, @CurrentUser() user: RequestUser): Promise<{ keys: KeyDto[] }> {
    return { keys: await this.keys.list({ userId: user.id, permissions: user.permissions }, parseWith(ListKeysQuerySchema, query)) };
  }

  @Post()
  @Auth({ permissions: USE })
  @ApiOperation({ summary: 'Create a key. The response carries the raw lai_ token once; it can never be shown again' })
  create(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<CreatedKeyDto> {
    return this.keys.create({ userId: user.id, permissions: user.permissions }, parseWith(CreateKeyBodySchema, body));
  }

  @Patch(':id')
  @Auth({ permissions: USE })
  @ApiOperation({ summary: 'Rename a key or narrow its models (owner); change limits or widen (gateway:admin)' })
  update(@Param('id') id: string, @Body() body: unknown, @CurrentUser() user: RequestUser): Promise<KeyDto> {
    return this.keys.update({ userId: user.id, permissions: user.permissions }, parseWith(KeyIdSchema, id), parseWith(PatchKeyBodySchema, body));
  }

  @Delete(':id')
  @HttpCode(200)
  @Auth({ permissions: USE })
  @ApiOperation({ summary: 'Revoke a key (owner or gateway:admin). Effective on the next request; revoking twice is fine' })
  revoke(@Param('id') id: string, @CurrentUser() user: RequestUser): Promise<KeyDto> {
    return this.keys.revoke({ userId: user.id, permissions: user.permissions }, parseWith(KeyIdSchema, id));
  }
}
