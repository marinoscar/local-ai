// The words of a command on the page (docs/specs/cloud-app.md section 3.11, issue #86): what it is, where it stands and
// what its result or its error means. queued -> "Waiting for ai-n2 (checks every few seconds)", running -> "Running",
// settled -> "Done" plus the result, or the plain-language fix of the error's code. Pure; never HTML.
import { plainError } from '../doctorFix';
import type { CommandView } from '../api/types';
import { formatBytes, formatElapsed } from './format';
import { stripPrefix } from './commandPolicy';

const str = (value: unknown): string => (typeof value === 'string' ? value : '');
const num = (value: unknown): number | null => (typeof value === 'number' && Number.isFinite(value) ? value : null);

/** "Load qwen3.5-9b-q4km", "Fetch the llama logs", ... */
export function describeCommand(type: string, params: Record<string, unknown> | null | undefined): string {
  const p = params ?? {};
  switch (stripPrefix(type)) {
    case 'status.refresh':
      return 'Refresh status';
    case 'model.load':
      return `Load ${str(p.model_id)}`;
    case 'model.unload':
      return `Unload ${str(p.model_id)}`;
    case 'model.set_default':
      return `Set ${str(p.model_id)} as the default model`;
    case 'model.download':
      return `Download ${str(p.model_id)}`;
    case 'model.delete':
      return `Delete the files of ${str(p.model_id)}`;
    case 'ark.sync':
      return p.dry_run === true ? 'Preview the ark sync (dry run)' : 'Sync the ark';
    case 'ark.verify':
      return 'Verify the ark';
    case 'mode.offline':
      return 'Go offline';
    case 'key.rotate':
      return 'Rotate the key';
    case 'service.restart':
      return `Restart ${str(p.service)}`;
    case 'logs.tail':
      return `Fetch the ${str(p.unit)} logs`;
    case 'catalog.refresh':
      return 'Refresh the catalog';
    case 'ensure.run':
      return "Install what's missing";
    case 'agent.update':
      return `Update the agent to ${str(p.version)}`;
    default:
      return stripPrefix(type);
  }
}

export const isActive = (command: Pick<CommandView, 'status'>): boolean => command.status === 'queued' || command.status === 'running';

/** The state of a command in one phrase. */
export function statusText(command: Pick<CommandView, 'status' | 'nodeId'>): string {
  switch (command.status) {
    case 'queued':
      return `Waiting for ${command.nodeId} (checks every few seconds)`;
    case 'running':
      return 'Running';
    case 'succeeded':
      return 'Done';
    case 'failed':
      return 'Failed';
    case 'expired':
      return 'Not picked up in time';
    case 'cancelled':
      return 'Cancelled';
  }
}

/** Seconds since the command was queued; once the node claimed it, since the claim (so a settled command shows how long it ran). */
export function elapsedSeconds(command: Pick<CommandView, 'status' | 'createdAt' | 'claimedAt' | 'finishedAt'>, now: Date): number {
  const start = new Date(command.status !== 'queued' && command.claimedAt ? command.claimedAt : command.createdAt).getTime();
  const end = command.finishedAt ? new Date(command.finishedAt).getTime() : now.getTime();
  return Math.max(0, (end - start) / 1000);
}

export const elapsedText = (command: Pick<CommandView, 'status' | 'createdAt' | 'claimedAt' | 'finishedAt'>, now: Date): string => formatElapsed(elapsedSeconds(command, now));

/** One line about a succeeded command's result (never the logs text: that is shown in the Logs tab). */
export function resultSummary(command: Pick<CommandView, 'type' | 'result'>): string | null {
  const r = command.result;
  if (!r) return null;
  switch (stripPrefix(command.type)) {
    case 'model.load': {
      const ms = num(r.load_ms);
      const unloaded = Array.isArray(r.unloaded) ? r.unloaded.length : 0;
      return [ms === null ? null : `loaded in ${formatElapsed(ms / 1000)}`, unloaded ? `${unloaded} other model${unloaded === 1 ? '' : 's'} unloaded` : null].filter(Boolean).join(', ') || null;
    }
    case 'model.unload':
      return r.was_loaded === false ? 'it was not loaded' : null;
    case 'model.set_default':
      return str(r.previous) ? `the previous default was ${str(r.previous)}` : null;
    case 'model.download':
      return r.already_present === true ? 'already on disk' : `${formatBytes(num(r.bytes_planned) ?? 0)} planned`;
    case 'model.delete': {
      const freed = num(r.freed_bytes) ?? 0;
      const kept = Array.isArray(r.kept_roots) ? r.kept_roots.length : 0;
      return `${formatBytes(freed)} freed${kept ? `, ${kept} copy${kept === 1 ? '' : ' copies'} kept` : ''}`;
    }
    case 'ark.sync': {
      const plan = (r.plan ?? {}) as Record<string, unknown>;
      const items = num(plan.items_to_download) ?? 0;
      const bytes = formatBytes(num(plan.bytes_to_download) ?? 0);
      return r.started === false ? 'a sync was already running' : `${items} item${items === 1 ? '' : 's'} to download (${bytes})`;
    }
    case 'ark.verify':
      return r.started === false ? 'a check was already running' : `${num(r.items_planned) ?? 0} items to check`;
    case 'key.rotate':
      return r.cloud_synced === false ? `new key ${str(r.fingerprint)}; the cloud will get it on the next retry` : `new key ${str(r.fingerprint)}`;
    case 'service.restart':
      return `${str(r.service)} is ${str(r.active_state)}`;
    case 'agent.update':
      return r.status === 'already_current' ? `already on ${str(r.to_version)}` : r.status === 'rolled_back' ? `rolled back to ${str(r.from_version)}` : `${str(r.from_version)} to ${str(r.to_version)}`;
    case 'ensure.run': {
      const pending = num(r.pending_steps) ?? 0;
      return r.started === true ? `installing (${pending} part${pending === 1 ? '' : 's'} left to do)` : r.state === 'complete' ? 'nothing was missing' : 'an install was already running, or nothing could be started';
    }
    case 'catalog.refresh':
      return r.accepted === false ? `the node kept its catalog (${str(r.rejected_code) || 'rejected'})` : r.changed === true ? `now on ${str(r.catalog_version)}` : 'already up to date';
    default:
      return null;
  }
}

/** The plain-language problem of a failed or expired command: meaning and fix from the registry (or the agent's own message). */
export function failureText(command: Pick<CommandView, 'error'>) {
  return plainError(command.error);
}
