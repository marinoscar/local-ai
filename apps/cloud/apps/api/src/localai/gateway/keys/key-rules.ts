// The rules of key management as pure functions (docs/specs/gateway.md section 3.8, issue #137 notes 2 to 4): who may
// create which key, with which limits and expiry, and who may change what. No I/O, so the permission matrix is a table in
// a unit test. Every refusal is an HTTP exception the service lets through.
import { BadRequestException, ForbiddenException } from '@nestjs/common';

import { LOCALAI_PERMISSIONS } from '../../localai.permissions';
import type { GatewayKeyDefaults } from '../../settings.service';
import type { KeyChanges } from './key.store';
import type { CreateKeyBody, PatchKeyBody } from './keys.schemas';

/** The signed-in caller, as far as the rules need to know. */
export interface KeyActor {
  userId: string;
  permissions: readonly string[];
}

export const DAY_MS = 86_400_000;

export const isGatewayAdmin = (actor: KeyActor): boolean => actor.permissions.includes(LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id);

/** The names without repeats, in first-seen order. */
export function dedupe(names: readonly string[]): string[] {
  return [...new Set(names)];
}

/**
 * Is `next` no wider than `previous`? An empty list means "every enabled model", so: from "all" to any list is
 * narrowing, from a list to "all" is widening, and from a list to a list it is narrowing when every entry was already there.
 */
export function isNarrowing(previous: readonly string[], next: readonly string[]): boolean {
  if (previous.length === 0) return true;
  if (next.length === 0) return false;
  const before = new Set(previous);
  return next.every((name) => before.has(name));
}

export interface ResolvedKey {
  kind: 'user' | 'service';
  ownerUserId: string;
  allowedModels: string[];
  rpm: number;
  concurrent: number;
  dailyTokens: number | null;
  expiresAt: Date | null;
}

/** Applies the creation rules to a request. Throws 400 or 403; answers the values to store. */
export function resolveCreate(body: CreateKeyBody, actor: KeyActor, defaults: GatewayKeyDefaults, now: Date): ResolvedKey {
  const admin = isGatewayAdmin(actor);
  const kind = body.kind ?? 'user';
  if (kind === 'service' && !admin) throw new ForbiddenException('Only a gateway administrator can create a service key');
  const ownerUserId = body.ownerUserId ?? actor.userId;
  if (ownerUserId !== actor.userId && !admin) throw new ForbiddenException('Only a gateway administrator can create a key for someone else');

  const base = kind === 'user' ? defaults.user : defaults.service;
  const rpm = body.limits?.rpm ?? base.rpm;
  const concurrent = body.limits?.concurrent ?? base.concurrent;
  const dailyTokens = body.limits?.dailyTokens !== undefined ? body.limits.dailyTokens : base.dailyTokens;

  let expiresAt: Date | null;
  if (kind === 'user') {
    if (dailyTokens === null) throw new BadRequestException('A user key needs a daily token budget');
    if (body.expiresInDays === null) throw new BadRequestException('A user key must expire');
    const days = body.expiresInDays ?? Math.min(defaults.user.expiresInDays, defaults.maxUserKeyDays);
    if (days < 1 || days > defaults.maxUserKeyDays) throw new BadRequestException(`expiresInDays must be between 1 and ${defaults.maxUserKeyDays}`);
    const above = rpm > defaults.user.rpm || concurrent > defaults.user.concurrent || dailyTokens > defaults.user.dailyTokens;
    if (above && !admin) throw new ForbiddenException('Only a gateway administrator can give a key higher limits than the default');
    expiresAt = new Date(now.getTime() + days * DAY_MS);
  } else {
    const days = body.expiresInDays !== undefined ? body.expiresInDays : base.expiresInDays;
    expiresAt = days === null ? null : new Date(now.getTime() + days * DAY_MS);
  }
  return { kind, ownerUserId, allowedModels: dedupe(body.allowedModels ?? []), rpm, concurrent, dailyTokens, expiresAt };
}

export interface ResolvedPatch {
  changes: KeyChanges;
  /** Fields that change, for the audit row (names only). */
  changed: string[];
  /** Entries of the new `allowedModels` that were not allowed before: these must be known models. */
  addedModels: string[];
}

/** The part of a stored key the patch rules read. */
export interface PatchTarget {
  userId: string;
  kind: 'user' | 'service';
  allowedModels: readonly string[];
}

/**
 * Applies the update rules. The owner may change the name and NARROW the models; limits and widening are for a gateway
 * administrator. Throws 400 or 403; answers the columns to write.
 */
export function resolvePatch(target: PatchTarget, body: PatchKeyBody, actor: KeyActor): ResolvedPatch {
  const admin = isGatewayAdmin(actor);
  const changes: KeyChanges = {};
  const changed: string[] = [];
  let addedModels: string[] = [];

  if (body.limits !== undefined && Object.keys(body.limits).length > 0 && !admin) {
    throw new ForbiddenException('Only a gateway administrator can change a key\'s limits');
  }
  if (body.name !== undefined) {
    changes.name = body.name;
    changed.push('name');
  }
  if (body.allowedModels !== undefined) {
    const next = dedupe(body.allowedModels);
    if (!admin && !isNarrowing(target.allowedModels, next)) throw new ForbiddenException('Only a gateway administrator can widen the models of a key');
    const before = new Set(target.allowedModels);
    addedModels = next.filter((name) => !before.has(name));
    changes.allowedModels = next;
    changed.push('allowedModels');
  }
  if (body.limits !== undefined) {
    if (body.limits.rpm !== undefined) {
      changes.rpmLimit = body.limits.rpm;
      changed.push('rpm');
    }
    if (body.limits.concurrent !== undefined) {
      changes.concurrentLimit = body.limits.concurrent;
      changed.push('concurrent');
    }
    if (body.limits.dailyTokens !== undefined) {
      if (body.limits.dailyTokens === null && target.kind === 'user') throw new BadRequestException('A user key needs a daily token budget');
      changes.dailyTokenBudget = body.limits.dailyTokens === null ? null : BigInt(body.limits.dailyTokens);
      changed.push('dailyTokens');
    }
  }
  return { changes, changed, addedModels };
}
