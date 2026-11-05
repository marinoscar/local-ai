// Drops the cached policies when ANY replica published a policy write: the writer already invalidated its own, this
// covers the others (docs/specs/gateway.md section 3.8).
import { Inject, Injectable, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { ModelDirectoryProvider } from '../models/resolve';
import { GATEWAY_POLICY_UPDATED_CHANNEL } from './policy-events';

@Injectable()
export class PolicyCacheListener implements OnModuleInit, OnModuleDestroy {
  private unsubscribe: (() => void) | null = null;

  constructor(
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    private readonly directory: ModelDirectoryProvider,
  ) {}

  onModuleInit(): void {
    this.unsubscribe = this.bus.subscribe(GATEWAY_POLICY_UPDATED_CHANNEL, () => this.directory.invalidate());
  }

  onModuleDestroy(): void {
    this.unsubscribe?.();
    this.unsubscribe = null;
  }
}
