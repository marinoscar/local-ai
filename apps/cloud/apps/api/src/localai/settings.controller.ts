// `GET /api/localai/settings` (lab:read) and `PUT /api/localai/settings` (docs/specs/cloud-app.md sections 3.9 and 3.10,
// docs/specs/gateway.md section 3.7). The body is a flat map of the dotted keys of the spec's table. `PUT` is open to a
// holder of either administrator permission, and the permission is then checked PER KEY PREFIX: `gateway.*` keys need
// `gateway:admin`, every other key `lab:admin`, a body mixing prefixes needs both (403 otherwise). So a gateway
// administrator needs no `lab:admin`, and `lab:admin` alone cannot change a `gateway.*` key.
import { Body, Controller, Get, Put } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from './localai.permissions';
import { LocalaiSettingsService } from './settings.service';
import { checkSettingsBody, visibleSettings } from './settings.schemas';

export interface SettingsResponse {
  /** Dotted key to value. */
  settings: Record<string, unknown>;
}

@ApiTags('localai-settings')
@Controller('localai/settings')
export class SettingsController {
  constructor(private readonly settings: LocalaiSettingsService) {}

  @Get()
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'The localai settings namespace as a flat map of dotted keys (the tailnet name only for administrators)' })
  async get(@CurrentUser() user: RequestUser): Promise<SettingsResponse> {
    return { settings: visibleSettings(await this.settings.readFlat(), user.permissions) };
  }

  @Put()
  @Auth({ anyPermissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id, LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id] })
  @ApiOperation({ summary: 'Set one or more localai settings. gateway.* keys need gateway:admin, every other key lab:admin; a mixed body needs both' })
  async put(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<SettingsResponse> {
    const values = checkSettingsBody(body, user.permissions);
    const flat = await this.settings.write(values, user.id);
    return { settings: visibleSettings(flat, user.permissions) };
  }
}
