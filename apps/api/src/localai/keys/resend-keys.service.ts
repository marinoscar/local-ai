// "Ask all nodes to re-send keys" (docs/specs/cloud-app.md sections 3.8 and 3.9, LAI-CLD-032): queues `status.refresh`
// on every enrolled node. The agent answers a `status.refresh` by pushing a fresh snapshot and re-reporting its llama
// key (section 3.7), which re-fills the credential store after `SECRETS_ENCRYPTION_KEY` was lost.
import { Inject, Injectable } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { LOCALAI_COMMAND_ENQUEUER, type LocalaiCommandEnqueuer } from '../commands/command-enqueuer';

@Injectable()
export class ResendKeysService {
  constructor(
    private readonly prisma: PrismaService,
    @Inject(LOCALAI_COMMAND_ENQUEUER) private readonly commands: LocalaiCommandEnqueuer,
  ) {}

  /**
   * One `status.refresh` per enrolled node (`pending` nodes never contacted the cloud and `retired` ones have no key;
   * neither is asked). An already active one is reused, so pressing the button twice queues nothing new. Returns the
   * command ids in node order.
   */
  async resendAll(actorUserId: string | null): Promise<string[]> {
    const nodes = await this.prisma.localaiNode.findMany({
      where: { phase: 'enrolled' },
      select: { nodeId: true },
      orderBy: { nodeId: 'asc' },
    });
    const commandIds: string[] = [];
    for (const node of nodes) {
      const command = await this.commands.enqueue(node.nodeId, 'status.refresh', {}, { userId: actorUserId }, 'system');
      commandIds.push(command.id);
    }
    return commandIds;
  }
}
