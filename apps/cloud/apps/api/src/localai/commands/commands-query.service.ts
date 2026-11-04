// The read side of the command API (docs/specs/cloud-app.md section 3.9): a node's command history and one command.
// Both are plain reads. `GET /commands/:id` is polled every 2 seconds by the UI while a command is active, so it is
// ONE indexed read (the primary key, with the node's lab id joined in); it never takes a lock and never writes.
import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { commandIdOf, type ListCommandsQuery } from './command-api.schemas';
import { unknownCommand, unknownLabNode } from './command-errors';
import { toCommandView, type CommandView } from './command-view';

export interface CommandPage {
  items: CommandView[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

@Injectable()
export class CommandsQueryService {
  constructor(private readonly prisma: PrismaService) {}

  /** The node's commands, newest first, optionally of one status. A retired node's history stays readable. */
  async list(labNodeId: string, query: ListCommandsQuery): Promise<CommandPage> {
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId: labNodeId }, select: { id: true, nodeId: true } });
    if (!node) throw unknownLabNode();
    const where = { nodeId: node.id, ...(query.status ? { status: query.status } : {}) };
    const [rows, total] = await Promise.all([
      this.prisma.localaiCommand.findMany({
        where,
        orderBy: [{ createdAt: 'desc' }, { id: 'desc' }],
        skip: (query.page - 1) * query.pageSize,
        take: query.pageSize,
      }),
      this.prisma.localaiCommand.count({ where }),
    ]);
    return {
      items: rows.map((row) => toCommandView(row, node.nodeId)),
      page: query.page,
      pageSize: query.pageSize,
      total,
      totalPages: Math.max(1, Math.ceil(total / query.pageSize)),
    };
  }

  /** One command with its result and error; 404 for an unknown id or one that is not a uuid (it never reaches SQL). */
  async get(rawId: string): Promise<CommandView> {
    const row = await this.prisma.localaiCommand.findUnique({ where: { id: commandIdOf(rawId) }, include: { node: { select: { nodeId: true } } } });
    if (!row) throw unknownCommand();
    return toCommandView(row, row.node.nodeId, { withResult: true });
  }
}
