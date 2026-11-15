// `MUSIC_AI` while the `ai` slice is mounted (#568, #570): every model call goes through `AiService.forUser(userId, {
// orgId, feature })`, which runs the platform's gates (kill switch, key policy, limits), resolves the key server-side
// for this one call and records the usage row (the feature and token counts, never the text). No provider SDK is
// imported and no key material reaches this file. Loaded with `require` by `music.module.ts` only when the slice is
// enabled, because it imports the AI package.
//
// "A model resolves for this person" is the AI slice's own `AiFeaturesService.listForUser` (the `localai_music`
// feature is `usable`: a model that writes text, on an enabled provider, with a key the policy lets this person
// spend). The AI module does not export that service to feature modules, so it is looked up in the application graph
// (`strict: false`); a call whose model or key does not resolve after all still answers "no model" (the AI error codes
// below).
import { Injectable } from '@nestjs/common';
import { ModuleRef } from '@nestjs/core';
import { AiConfigService, AiFeaturesService, AiService, registerAiFeature } from '@marinoscar/platform-api/ai';

import { MUSIC_AI_FEATURE, MusicAiUnavailable, type MusicAiAsk, type MusicAiCaller, type MusicAiPort } from './music-ai';

/**
 * The feature as the AI Usage page and `GET /api/ai/features` show it. The registry takes ONE label (no Spanish one:
 * a missing platform seam, reported on #570); the web shows "Música: letras y títulos" itself.
 */
export const MUSIC_AI_FEATURE_LABEL = 'Music: lyrics and titles';

registerAiFeature({ id: MUSIC_AI_FEATURE, label: MUSIC_AI_FEATURE_LABEL, group: 'Local AI', needs: ['responses'] });

/** The AI errors that mean "no model or key for this person", not "the call failed". */
const NO_MODEL_CODES = new Set(['AI_KEY_REQUIRED', 'AI_MODEL_REQUIRED', 'AI_MODEL_NOT_ENABLED', 'AI_MODEL_NOT_REACHABLE', 'AI_PROVIDER_DISABLED', 'AI_CAPABILITY_UNSUPPORTED']);

const orgOf = (caller: MusicAiCaller): string | undefined => caller.orgId ?? undefined;

@Injectable()
export class AiServiceMusicAi implements MusicAiPort {
  constructor(
    private readonly ai: AiService,
    private readonly config: AiConfigService,
    private readonly moduleRef: ModuleRef,
  ) {}

  async aiEnabled(caller: MusicAiCaller): Promise<boolean> {
    return (await this.config.resolveForOrg(orgOf(caller))).enabled;
  }

  async modelResolves(caller: MusicAiCaller): Promise<boolean> {
    const features = this.features();
    if (!features) return true;
    const views = await features.listForUser(caller.userId, orgOf(caller));
    return views.some((view) => view.id === MUSIC_AI_FEATURE && view.usable);
  }

  async ask(caller: MusicAiCaller, ask: MusicAiAsk): Promise<string> {
    try {
      const answer = await this.ai
        .forUser(caller.userId, { feature: MUSIC_AI_FEATURE, ...(caller.orgId ? { orgId: caller.orgId } : {}) })
        .respond({ instructions: ask.instructions, input: ask.input, maxOutputTokens: ask.maxOutputTokens });
      return answer.outputText;
    } catch (error) {
      const code = (error as { code?: unknown }).code;
      if (code === 'AI_DISABLED') throw new MusicAiUnavailable('ai_off');
      if (typeof code === 'string' && NO_MODEL_CODES.has(code)) throw new MusicAiUnavailable('no_model');
      throw error;
    }
  }

  private features(): AiFeaturesService | null {
    try {
      return this.moduleRef.get(AiFeaturesService, { strict: false });
    } catch {
      return null;
    }
  }
}
