// The AiService binding of the music helpers (#568): availability is "AI on AND a text model resolves for this user",
// and a call goes through `AiService.forUser(userId, { feature: 'localai_music' }).respond(...)`, never an SDK. The
// feature is registered with the AI platform. `NoMusicAi` (no AI slice) is always off.
import type { ModuleRef } from '@nestjs/core';
import { UsableModelsService, getAiFeature, type AiConfigService, type AiService } from '@marinoscar/platform-api/ai';

import { MUSIC_AI_FEATURE, MusicAiUnavailable, NoMusicAi } from './music-ai';
import { AiServiceMusicAi } from './music-ai.adapter';

const model = (capabilities: string[]) => ({ provider: 'p', modelId: 'm', displayName: null, capabilities: { capabilities, inputModalities: [], outputModalities: [] }, keySource: 'user' });

function adapter(options: { enabled: boolean; models: unknown[] | null; answer?: string; error?: unknown }) {
  const respond = options.error ? jest.fn().mockRejectedValue(options.error) : jest.fn().mockResolvedValue({ outputText: options.answer ?? '' });
  const forUser = jest.fn().mockReturnValue({ respond });
  const ai = { forUser } as unknown as AiService;
  const config = { isEnabled: jest.fn().mockResolvedValue(options.enabled) } as unknown as AiConfigService;
  const models = { listForUser: jest.fn().mockResolvedValue(options.models ?? []) };
  const moduleRef = {
    get: jest.fn((token: unknown) => {
      if (token !== UsableModelsService || options.models === null) throw new Error('not found');
      return models;
    }),
  } as unknown as ModuleRef;
  return { port: new AiServiceMusicAi(ai, config, moduleRef), forUser, respond };
}

describe('AiServiceMusicAi', () => {
  it('registers the music feature with the AI platform', () => {
    expect(getAiFeature(MUSIC_AI_FEATURE)).toMatchObject({ id: 'localai_music', needs: ['responses'] });
  });

  it('is off when AI is off, without a model that answers text, and on otherwise', async () => {
    expect(await adapter({ enabled: false, models: [model(['responses'])] }).port.availability('u1')).toEqual({ available: false, reason: 'ai_off' });
    expect(await adapter({ enabled: true, models: [] }).port.availability('u1')).toEqual({ available: false, reason: 'no_model' });
    expect(await adapter({ enabled: true, models: [model(['embeddings'])] }).port.availability('u1')).toEqual({ available: false, reason: 'no_model' });
    expect(await adapter({ enabled: true, models: [model(['responses'])] }).port.availability('u1')).toEqual({ available: true, reason: null });
    // Without the slice's model service in the graph, "on" is decided by the call itself.
    expect(await adapter({ enabled: true, models: null }).port.availability('u1')).toEqual({ available: true, reason: null });
  });

  it('turns "no key or model" and "AI off" errors into MusicAiUnavailable, and passes other errors on', async () => {
    const coded = (code: string) => Object.assign(new Error('x'), { code });
    await expect(adapter({ enabled: true, models: [], error: coded('AI_KEY_REQUIRED') }).port.ask('u1', { instructions: '', input: '', maxOutputTokens: 1 })).rejects.toMatchObject({ reason: 'no_model' });
    await expect(adapter({ enabled: true, models: [], error: coded('AI_DISABLED') }).port.ask('u1', { instructions: '', input: '', maxOutputTokens: 1 })).rejects.toMatchObject({ reason: 'ai_off' });
    await expect(adapter({ enabled: true, models: [], error: coded('AI_PROVIDER_ERROR') }).port.ask('u1', { instructions: '', input: '', maxOutputTokens: 1 })).rejects.toMatchObject({ code: 'AI_PROVIDER_ERROR' });
  });

  it('asks through AiService.forUser with the feature id and returns the text', async () => {
    const { port, forUser, respond } = adapter({ enabled: true, models: [], answer: 'Harbour Lights' });
    await expect(port.ask('u1', { instructions: 'name it', input: 'text', maxOutputTokens: 40 })).resolves.toBe('Harbour Lights');
    expect(forUser).toHaveBeenCalledWith('u1', { feature: 'localai_music' });
    expect(respond).toHaveBeenCalledWith({ instructions: 'name it', input: 'text', maxOutputTokens: 40 });
  });
});

describe('NoMusicAi', () => {
  it('is always off and refuses every call', async () => {
    const port = new NoMusicAi();
    expect(await port.availability()).toEqual({ available: false, reason: 'ai_off' });
    await expect(port.ask()).rejects.toBeInstanceOf(MusicAiUnavailable);
  });
});
