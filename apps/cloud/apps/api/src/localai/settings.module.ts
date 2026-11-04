// The `localai` settings route and reader. `SystemSettingsService` comes from the platform's global `SettingsModule`.
import { Module } from '@nestjs/common';

import { SettingsController } from './settings.controller';
import { LocalaiSettingsService } from './settings.service';

@Module({ controllers: [SettingsController], providers: [LocalaiSettingsService], exports: [LocalaiSettingsService] })
export class LocalaiSettingsModule {}
