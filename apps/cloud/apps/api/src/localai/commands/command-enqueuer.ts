// The seam through which other Local AI features queue a command for a node (docs/specs/cloud-app.md section 3.7).
// `CommandsService` (the command plane, CLOUD-08) implements it; `CommandEnqueuerModule` binds the token to it, so a
// caller (the "Ask all nodes to re-send keys" action, later the UI routes and the gateway) never imports the service.

/** Injection token of the {@link LocalaiCommandEnqueuer}. */
export const LOCALAI_COMMAND_ENQUEUER = Symbol('LOCALAI_COMMAND_ENQUEUER');

/**
 * Who queued the command: a person's action (`ui`), the API itself (`system`), the gateway (`gateway`) or a catalog
 * approval that asked the nodes to refresh (`catalog_approve`). Stored in `localai_commands.source`.
 */
export type CommandSource = 'ui' | 'system' | 'gateway' | 'catalog_approve';

export interface EnqueuedCommand {
  /** `localai_commands.id`. */
  id: string;
  /** False when an active command of the same type and subject already existed and was returned instead. */
  created: boolean;
}

export interface LocalaiCommandEnqueuer {
  /**
   * Queues `type` (without the `localai.` prefix, for example `status.refresh`) for the node with lab id `nodeId`
   * (`ai-n3`). The params are validated by the type's strict schema (a 400 with the Zod issues otherwise); an active
   * duplicate (queued or running, same dedup key) is returned instead of a second row.
   */
  enqueue(nodeId: string, type: string, rawParams: unknown, actor: { userId: string | null }, source: CommandSource): Promise<EnqueuedCommand>;
}
