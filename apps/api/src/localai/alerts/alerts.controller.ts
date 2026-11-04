// The alert routes of the cloud UI (docs/specs/cloud-app.md section 3.9): the list needs `lab:read`, the acknowledgement
// `lab:operate`. A viewer therefore reads the Alerts page and never sees an Ack button work. No other route here.
import { Controller, Get, HttpCode, Param, Post, Query } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { AlertsService } from './alerts.service';
import { AlertIdSchema, ListAlertsQuerySchema, parseWith, toAlertDto, type AlertDto, type AlertsResponse } from './alerts.schemas';

@ApiTags('localai-alerts')
@Controller('localai/alerts')
export class AlertsController {
  constructor(private readonly alerts: AlertsService) {}

  @Get()
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'Alerts, most urgent first. ?state=active (default) | cleared | all, ?nodeId=ai-n3, ?limit=' })
  async list(@Query() query: unknown): Promise<AlertsResponse> {
    const { state, nodeId, limit } = parseWith(ListAlertsQuerySchema, query);
    const records = await this.alerts.list({ state, limit, ...(nodeId !== undefined ? { nodeId } : {}) });
    return { alerts: records.map(toAlertDto) };
  }

  @Post(':id/ack')
  @HttpCode(200)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_OPERATE.id] })
  @ApiOperation({ summary: 'Acknowledge an active alert: stops its repeat notifications, does not clear it' })
  async acknowledge(@Param('id') id: string, @CurrentUser() user: RequestUser): Promise<AlertDto> {
    const record = await this.alerts.acknowledge(parseWith(AlertIdSchema, id), user.id);
    return toAlertDto(record);
  }
}
