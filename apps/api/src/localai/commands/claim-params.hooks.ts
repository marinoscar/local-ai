// Claim-time params (E11): a command type may finish its params at the moment a node claims it. `music.generate` uses it
// to mint its signed storage URLs then, so no signed URL is ever stored in `localai_commands.params` (where the history,
// a backup or a database reader could see it) and each lives only for the command's hard cap.
//
// The command plane owns the seam; a feature registers one hook per type when its module starts. A hook that fails
// leaves the stored params as they are (logged, ids only): the node then fails the command with the feature's own
// code, rather than the claim failing for every other command of the node.
import { Injectable, Logger } from '@nestjs/common';

import type { CommandType } from '../contracts/commands';

/** Turns the stored params of a claimed command into the params the node receives. */
export type ClaimParamsHook = (params: Record<string, unknown>, commandId: string) => Promise<Record<string, unknown>>;

@Injectable()
export class ClaimParamsHooks {
  private readonly logger = new Logger(ClaimParamsHooks.name);
  private readonly hooks = new Map<CommandType, ClaimParamsHook>();

  /** Registers the hook of `type`; a second registration for the same type is a programming error. */
  register(type: CommandType, hook: ClaimParamsHook): void {
    if (this.hooks.has(type)) throw new Error(`A claim params hook is already registered for ${type}`);
    this.hooks.set(type, hook);
  }

  /** The params a node receives for a claimed command of `type`. Never throws. */
  async apply(type: string, params: Record<string, unknown>, commandId: string): Promise<Record<string, unknown>> {
    const hook = this.hooks.get(type as CommandType);
    if (!hook) return params;
    try {
      return await hook(params, commandId);
    } catch (error) {
      this.logger.error(`claim params hook for ${type} failed for command_id=${commandId} (${error instanceof Error ? error.name : 'error'})`);
      return params;
    }
  }
}
