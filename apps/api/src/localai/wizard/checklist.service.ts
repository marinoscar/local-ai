// The publish checklist of a node (docs/specs/cloud-app.md section 4.8): reads it with the commands and files rendered
// for that node, and applies an operator's action (mark done, mark to do, or run the automatic check).
//
// WHERE THE REAL TAILNET ADDRESS GOES. `get` renders the snippets with the node's real tailnet address and name; the
// result is returned to a lab:admin and nowhere else. `act` stores `{ state, checkedAt, detail }` only, and `detail` is
// built from fixed sentences by `checklist.checks.ts`. The audit row carries the step, the action and the resulting
// state. Nothing here logs a host's addresses, a snippet or the node's key.
import { BadRequestException, ConflictException, Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import type { LocalaiNode, Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { NodeKeyService, NodeKeyUnreadableError } from '../keys/node-key.service';
import { checkCertificate, checkDns, checkGateway, checkHttpsVhost, type CheckResult, type GatewayKey } from './checklist.checks';
import {
  entryFromCheck,
  entryFromManual,
  isComplete,
  parseStoredChecklist,
  refusalFor,
  viewSteps,
  type ChecklistStepView,
  type StoredChecklist,
} from './checklist.state';
import { NETWORK_PROBE, type NetworkProbe } from './network-probe.port';
import { reservedEntries } from '../vps/vps-journal';
import { PUBLIC_HOST_RE } from './node-id';
import { renderStepSnippets, type MissingValue, type Snippet } from './snippets';
import { WizardSettingsService } from './wizard-settings';
import type { ChecklistActionBody, ChecklistStep } from './wizard.schemas';

/** Audit action (permanent string, spec section 3.5). */
export const AUDIT_CHECKLIST_UPDATED = 'localai.checklist.updated';

export interface ChecklistStepDto extends ChecklistStepView {
  title: string;
  /** The commands and files for this step, with the node's real tailnet address. lab:admin only. */
  snippets: Snippet[];
  /** Values the node does not have yet, left as placeholders in `snippets`. */
  missing: MissingValue[];
}

export interface ChecklistDto {
  nodeId: string;
  publicHost: string;
  servePattern: 'A' | 'B' | 'none';
  /** All eight steps done: the page says the node is part of the lab. */
  complete: boolean;
  steps: ChecklistStepDto[];
}

const TITLES: Readonly<Record<ChecklistStep, (node: Pick<LocalaiNode, 'nodeId' | 'publicHost' | 'servePattern'>) => string>> = {
  tailnet: () => 'Node is on the tailnet (tailscale up)',
  dns: (node) => `DNS: ${node.publicHost} A → VPS`,
  bootstrap_vhost: () => 'Bootstrap HTTP vhost',
  certificate: () => 'Certificate issued',
  https_vhost: (node) => `Final HTTPS vhost (Pattern ${node.servePattern})`,
  enrolled: () => 'Node enrolled',
  key: () => 'llama key received by the cloud',
  gateway_test: (node) => `Gateway test: /v1/models through ${node.nodeId}`,
};

@Injectable()
export class ChecklistService {
  private readonly logger = new Logger(ChecklistService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly settings: WizardSettingsService,
    private readonly nodeKeys: NodeKeyService,
    @Inject(NETWORK_PROBE) private readonly probe: NetworkProbe,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  async get(nodeId: string): Promise<ChecklistDto> {
    return this.render(await this.load(nodeId));
  }

  /** Applies one action to one step and returns the whole checklist as it is afterwards. */
  async act(nodeId: string, step: ChecklistStep, body: ChecklistActionBody, actorUserId: string | null, now: Date = new Date()): Promise<ChecklistDto> {
    const node = await this.load(nodeId);
    const refusal = refusalFor(step, body.action);
    if (refusal) {
      throw new BadRequestException({ message: refusalMessage(refusal, step), details: { reason: refusal, step } });
    }

    const entry = body.action === 'check' ? entryFromCheck(await this.runCheck(node, step, now), now) : entryFromManual(body.action, now);
    await this.store(node.id, step, entry);
    await this.record(nodeId, actorUserId, { step, action: body.action, state: entry.state });
    return this.get(nodeId);
  }

  private async load(nodeId: string): Promise<LocalaiNode> {
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId } });
    if (!node) throw new NotFoundException({ message: 'No Local AI node has this id', details: { reason: 'unknown_localai_node' } });
    if (node.phase === 'retired') {
      throw new ConflictException({ message: 'This node is retired', details: { code: 'LAI-CLD-035', reason: 'node_retired' } });
    }
    return node;
  }

  private async render(node: LocalaiNode): Promise<ChecklistDto> {
    const settings = await this.settings.read();
    const stored = parseStoredChecklist(node.publishChecklist);
    const views = viewSteps(stored, { phase: node.phase, keyReportedAt: node.keyReportedAt });
    const snippetNode = {
      nodeId: node.nodeId,
      publicHost: node.publicHost,
      servePattern: node.servePattern,
      tailnetMachine: node.tailnetMachine,
      tailnetIp: node.tailnetIp,
    };
    const steps = views.map((view) => {
      const rendered = renderStepSnippets(view.step, snippetNode, settings.vpsPublicHostname);
      return { ...view, title: TITLES[view.step](node), snippets: rendered.snippets, missing: rendered.missing };
    });
    return { nodeId: node.nodeId, publicHost: node.publicHost, servePattern: node.servePattern, complete: isComplete(views), steps };
  }

  /** Runs the automatic check of `step`. Never throws for a network problem: that is a `failed` result with a plain detail. */
  private async runCheck(node: LocalaiNode, step: ChecklistStep, now: Date): Promise<CheckResult> {
    // The host goes into URLs and a DNS query: only a plain DNS name is probed.
    if (!PUBLIC_HOST_RE.test(node.publicHost)) {
      return { state: 'failed', detail: 'The public host is not a valid DNS name. Fix it in the node’s Settings, then press Check again.' };
    }
    switch (step) {
      case 'dns':
        return checkDns(this.probe, node.publicHost, (await this.settings.read()).vpsPublicHostname);
      case 'certificate':
        return checkCertificate(this.probe, node.publicHost, now);
      case 'https_vhost':
        return checkHttpsVhost(this.probe, node.publicHost);
      case 'gateway_test':
        return this.gatewayTest(node);
      default:
        throw new Error(`step ${step} has no automatic check`); // unreachable: refusalFor admits `check` on check steps only
    }
  }

  private async gatewayTest(node: LocalaiNode): Promise<CheckResult> {
    const key = await this.readKey(node.nodeId);
    const loaded = await this.prisma.localaiNodeModel.findMany({ where: { nodeId: node.id, status: 'loaded' }, select: { modelId: true } });
    return checkGateway(this.probe, node.publicHost, key, loaded.map((row) => row.modelId));
  }

  /** The stored llama key, server-side only. It goes into the check's one request header and nowhere else. */
  private async readKey(nodeId: string): Promise<GatewayKey> {
    try {
      const key = await this.nodeKeys.getKey(nodeId);
      return key === null ? { kind: 'missing' } : { kind: 'key', key };
    } catch (error) {
      if (error instanceof NodeKeyUnreadableError) return { kind: 'unreadable' };
      throw error;
    }
  }

  /** Merges one step into the stored JSON under a row lock, so two steps saved at once both survive. */
  private async store(rowId: string, step: ChecklistStep, entry: StoredChecklist[ChecklistStep]): Promise<void> {
    await this.prisma.$transaction(async (tx) => {
      await tx.$queryRaw`SELECT id FROM localai_nodes WHERE id = ${rowId}::uuid FOR UPDATE`;
      const current = await tx.localaiNode.findUniqueOrThrow({ where: { id: rowId }, select: { publishChecklist: true } });
      // The stack-agent's journal lives beside the steps (key `vps`) and must survive a manual action.
      const merged = { ...reservedEntries(current.publishChecklist), ...parseStoredChecklist(current.publishChecklist), [step]: entry };
      await tx.localaiNode.update({ where: { id: rowId }, data: { publishChecklist: merged as unknown as Prisma.InputJsonObject } });
    });
  }

  /** After the write committed, outside the transaction. A failed audit is logged by name only. */
  private async record(nodeId: string, actorUserId: string | null, meta: { step: ChecklistStep; action: string; state: string }): Promise<void> {
    try {
      await this.audit.record({ action: AUDIT_CHECKLIST_UPDATED, actorUserId, targetType: 'localai_node', targetId: nodeId, meta });
    } catch (error) {
      this.logger.error(`audit ${AUDIT_CHECKLIST_UPDATED} failed for node_id=${nodeId}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }
}

function refusalMessage(refusal: string, step: ChecklistStep): string {
  switch (refusal) {
    case 'automatic_step':
      return `The ${step} step completes by itself; there is nothing to press.`;
    case 'not_a_manual_step':
      return `The ${step} step is completed with its Check button, not by marking it done.`;
    default:
      return `The ${step} step has no automatic check; mark it done when you have done it.`;
  }
}
