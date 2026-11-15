// `MUSIC_AI` while the `ai` slice is mounted (#568): every model call goes through `AiService.forUser(userId, {
// feature })`, which runs the platform's gates (kill switch, key policy, limits), resolves the key server-side for this
// one call and records the usage row. No provider SDK is imported and no key material reaches this file. Loaded with
// `require` by `music.module.ts` only when the slice is enabled, because it imports the AI package.
//
// "A model resolves for this user" is the AI slice's own `UsableModelsService.listForUser` (a model that answers text,
// on an enabled provider, with a key the policy lets this user spend). The AI module does not export that service to
// feature modules, so it is looked up in the application graph (`strict: false`); a call whose model or key does not
// resolve after all still answers "no model" (the AI error codes below).
import { Injectable } from '@nestjs/common';
import { ModuleRef } from '@nestjs/core';
import { AiConfigService, AiService, UsableModelsService, registerAiFeature } from '@marinoscar/platform-api/ai';

import { MUSIC_AI_FEATURE, MusicAiUnavailable, type MusicAiAsk, type MusicAiAvailability, type MusicAiPort } from './music-ai';

registerAiFeature({ id: MUSIC_AI_FEATURE, label: 'Music: song titles and lyrics', group: 'Local AI', needs: ['responses'] });

/** The AI errors that mean "no model or key for this person", not "the call failed". */
const NO_MODEL_CODES = new Set(['AI_KEY_REQUIRED', 'AI_MODEL_REQUIRED', 'AI_MODEL_NOT_ENABLED', 'AI_PROVIDER_DISABLED', 'AI_CAPABILITY_UNSUPPORTED']);

@Injectable()
export class AiServiceMusicAi implements MusicAiPort {
  constructor(
    private readonly ai: AiService,
    private readonly config: AiConfigService,
    private readonly moduleRef: ModuleRef,
  ) {}

  async availability(userId: string): Promise<MusicAiAvailability> {
    if (!(await this.config.isEnabled())) return { available: false, reason: 'ai_off' };
    const models = this.usableModels();
    if (!models) return { available: true, reason: null };
    const usable = await models.listForUser(userId);
    return usable.some((model) => model.capabilities.capabilities.includes('responses')) ? { available: true, reason: null } : { available: false, reason: 'no_model' };
  }

  async ask(userId: string, ask: MusicAiAsk): Promise<string> {
    try {
      const answer = await this.ai.forUser(userId, { feature: MUSIC_AI_FEATURE }).respond({ instructions: ask.instructions, input: ask.input, maxOutputTokens: ask.maxOutputTokens });
      return answer.outputText;
    } catch (error) {
      const code = (error as { code?: unknown }).code;
      if (code === 'AI_DISABLED') throw new MusicAiUnavailable('ai_off');
      if (typeof code === 'string' && NO_MODEL_CODES.has(code)) throw new MusicAiUnavailable('no_model');
      throw error;
    }
  }

  private usableModels(): UsableModelsService | null {
    try {
      return this.moduleRef.get(UsableModelsService, { strict: false });
    } catch {
      return null;
    }
  }
}
