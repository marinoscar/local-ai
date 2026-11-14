// Marks a music request's songs ready (or the request failed) when its command settles. It listens to
// `localai.command.settled`, which the command plane publishes once per command after the settling write committed,
// exactly like the command audit listener: the work is two conditional database writes, outside any transaction, and
// a failure is logged (ids only) and never breaks the bus. Reads reconcile too, so a lost event only delays a song.
import { Inject, Injectable, Logger, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { COMMAND_SETTLED_CHANNEL, type CommandSettledEvent } from '../commands/command-events';
import { MusicService } from './music.service';

@Injectable()
export class MusicSettledListener implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(MusicSettledListener.name);
  private unsubscribe: (() => void) | null = null;

  constructor(
    private readonly music: MusicService,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
  ) {}

  onModuleInit(): void {
    this.unsubscribe = this.bus.subscribe<CommandSettledEvent>(COMMAND_SETTLED_CHANNEL, (event) => {
      void this.handleSettled(event);
    });
  }

  onModuleDestroy(): void {
    this.unsubscribe?.();
    this.unsubscribe = null;
  }

  /** Reconciles the request the command carries, if any. Never throws. */
  async handleSettled(event: CommandSettledEvent): Promise<void> {
    if (!event || typeof event.commandId !== 'string') return;
    try {
      await this.music.reconcileByCommand(event.commandId);
    } catch (error) {
      this.logger.error(`music reconcile failed for command_id=${event.commandId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }
}
