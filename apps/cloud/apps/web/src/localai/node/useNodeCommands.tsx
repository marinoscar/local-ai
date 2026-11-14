// The commands of one node as the page sees them (docs/specs/cloud-app.md sections 3.9 and 4.4). A button never talks
// to the node: `send` queues a closed-type command through the command API and this provider polls
// `GET /api/localai/commands/:id` every 2 s while it is queued or running (only while the tab is visible), then tells the
// page so it can refresh the node. Settled commands stay on screen until dismissed, so the operator reads the result
// or the fix. Commands already active when the page opens (queued from another tab, or before a reload) are adopted.
import { ApiError } from '@marinoscar/platform-web/core';
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';

import { cancelCommand, getCommand, listCommands, sendCommand } from '../api/commands';
import type { CommandView } from '../api/types';
import { offlineReason, stripPrefix } from './commandPolicy';
import { isActive } from './commandText';

/** How often an active command is read (spec 3.9). */
export const COMMAND_POLL_MS = 2000;

export type SendOutcome = { ok: true; command: CommandView } | { ok: false; message: string; code: string | null };

export interface NodeCommands {
  /** Newest first; includes settled commands the operator has not dismissed. */
  commands: CommandView[];
  /** The command in flight that matches `type` (and `subject`: model id, service or unit), if any. */
  activeFor: (type: string, subject?: string) => CommandView | undefined;
  /** The newest command of `type`, settled or not. */
  latest: (type: string) => CommandView | undefined;
  /** Queues a command. A refusal is also shown in the page's notice unless `silent` (a dialog shows it itself). */
  send: (type: string, params?: Record<string, unknown>, options?: { silent?: boolean }) => Promise<SendOutcome>;
  /**
   * Queues a command through a feature route of its own (the workload switch, `POST /nodes/:id/workloads/:name/activate`)
   * and tracks it like `send`: polled, shown, its refusal in the notice.
   */
  run: (queue: () => Promise<CommandView>, options?: { silent?: boolean }) => Promise<SendOutcome>;
  /** The latest refusal not yet closed (plain language, with the doctor code), or null. */
  notice: string | null;
  clearNotice: () => void;
  cancel: (id: string) => Promise<SendOutcome>;
  dismiss: (id: string) => void;
}

const Context = createContext<NodeCommands | null>(null);

export function useNodeCommands(): NodeCommands {
  const value = useContext(Context);
  if (!value) throw new Error('useNodeCommands needs a NodeCommandsProvider');
  return value;
}

/** What the API's refusal means for the operator: the doctor-code texts of spec 3.9 first, then the server's message. */
export function refusalText(error: unknown, nodeId: string): { message: string; code: string | null } {
  if (error instanceof ApiError) {
    const details = (error.details ?? {}) as { code?: unknown; reason?: unknown; mode?: unknown; issues?: unknown };
    const code = typeof details.code === 'string' ? details.code : (error.code ?? null);
    if (code === 'LAI-CLD-034') return { code, message: offlineReason(details.mode === 'survival' ? 'survival' : 'offline') };
    if (code === 'LAI-CLD-035') return { code, message: 'This node is not enrolled yet, or it was retired. Finish installing it on the computer first.' };
    if (error.status === 403) return { code, message: 'You do not have permission to do this.' };
    if (error.status === 404) return { code, message: 'This command or node no longer exists.' };
    if (error.status === 409) return { code, message: error.message || 'The node already took this command.' };
    if (error.status === 422 && details.reason === 'model_not_on_node') return { code, message: `This model is not on ${nodeId}. Download it first.` };
    return { code, message: error.message || 'The cloud refused the command.' };
  }
  return { code: null, message: 'Could not reach the cloud. Check your connection and try again.' };
}

const byNewest = (a: CommandView, b: CommandView) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime();

function upsert(list: CommandView[], command: CommandView): CommandView[] {
  const without = list.filter((item) => item.id !== command.id);
  return [command, ...without].sort(byNewest);
}

export interface NodeCommandsProviderProps {
  nodeId: string;
  /** Called once when a tracked command settles (the page refreshes the node). */
  onSettled?: (command: CommandView) => void;
  /** Called after a command was queued (the page may refresh too). */
  onQueued?: (command: CommandView) => void;
  pollMs?: number;
  children: ReactNode;
}

export function NodeCommandsProvider({ nodeId, onSettled, onQueued, pollMs = COMMAND_POLL_MS, children }: NodeCommandsProviderProps) {
  const [commands, setCommands] = useState<CommandView[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const mounted = useRef(true);
  const settledSeen = useRef(new Set<string>());
  const onSettledRef = useRef(onSettled);
  onSettledRef.current = onSettled;
  const onQueuedRef = useRef(onQueued);
  onQueuedRef.current = onQueued;
  const commandsRef = useRef(commands);
  commandsRef.current = commands;

  const track = useCallback((command: CommandView) => {
    if (!mounted.current) return;
    setCommands((current) => upsert(current, command));
    if (!isActive(command) && !settledSeen.current.has(command.id)) {
      settledSeen.current.add(command.id);
      onSettledRef.current?.(command);
    }
  }, []);

  // Adopt the commands that are already active (queued before a reload, or from another tab).
  useEffect(() => {
    mounted.current = true;
    let cancelled = false;
    listCommands(nodeId)
      .then((page) => {
        if (cancelled || !mounted.current) return;
        setCommands((current) => page.items.filter(isActive).reduce((list, item) => (list.some((c) => c.id === item.id) ? list : upsert(list, item)), current));
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      mounted.current = false;
    };
  }, [nodeId]);

  const hasActive = commands.some(isActive);
  useEffect(() => {
    if (!hasActive) return undefined;
    let timer: ReturnType<typeof setInterval> | null = null;
    const poll = async () => {
      const active = commandsRef.current.filter(isActive);
      await Promise.all(
        active.map(async (command) => {
          try {
            track(await getCommand(command.id));
          } catch {
            // A failed read keeps the last status on screen; the next tick tries again.
          }
        }),
      );
    };
    const stop = () => {
      if (timer !== null) clearInterval(timer);
      timer = null;
    };
    const start = () => {
      stop();
      timer = setInterval(() => void poll(), pollMs);
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') stop();
      else {
        void poll();
        start();
      }
    };
    if (document.visibilityState !== 'hidden') start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [hasActive, pollMs, track]);

  const run = useCallback(
    async (queue: () => Promise<CommandView>, options: { silent?: boolean } = {}): Promise<SendOutcome> => {
      try {
        const command = await queue();
        track(command);
        setNotice(null);
        onQueuedRef.current?.(command);
        return { ok: true, command };
      } catch (error) {
        const refusal = refusalText(error, nodeId);
        if (!options.silent && mounted.current) setNotice(refusal.code ? `${refusal.message} (${refusal.code})` : refusal.message);
        return { ok: false, ...refusal };
      }
    },
    [nodeId, track],
  );

  const send = useCallback(
    (type: string, params: Record<string, unknown> = {}, options: { silent?: boolean } = {}): Promise<SendOutcome> =>
      run(() => sendCommand(nodeId, stripPrefix(type), params), options),
    [nodeId, run],
  );

  const cancel = useCallback(
    async (id: string): Promise<SendOutcome> => {
      try {
        const command = await cancelCommand(id);
        track(command);
        return { ok: true, command };
      } catch (error) {
        // 409: the node claimed it first. Read the truth so the line shows "Running" instead of a stale "Waiting".
        try {
          track(await getCommand(id));
        } catch {
          // keep the last status
        }
        const refusal = refusalText(error, nodeId);
        const message = error instanceof ApiError && error.status === 409 ? 'The node already took this command, so it can no longer be cancelled.' : refusal.message;
        if (mounted.current) setNotice(message);
        return { ok: false, ...refusal, message };
      }
    },
    [nodeId, track],
  );

  const dismiss = useCallback((id: string) => setCommands((current) => current.filter((command) => command.id !== id)), []);

  const value = useMemo<NodeCommands>(
    () => ({
      commands,
      activeFor: (type, subject) =>
        commands.find((command) => {
          if (!isActive(command) || stripPrefix(command.type) !== stripPrefix(type)) return false;
          if (subject === undefined) return true;
          const p = command.params;
          return p.model_id === subject || p.service === subject || p.unit === subject || p.name === subject;
        }),
      latest: (type) => commands.find((command) => stripPrefix(command.type) === stripPrefix(type)),
      send,
      run,
      notice,
      clearNotice: () => setNotice(null),
      cancel,
      dismiss,
    }),
    [commands, send, run, notice, cancel, dismiss],
  );

  return <Context.Provider value={value}>{children}</Context.Provider>;
}
