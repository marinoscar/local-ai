// The AiService binding of the music helpers (#568, #570): "AI on" is the AI slice's switch for the organization,
// "a model resolves" is the `localai_music` feature being usable for the person, and a call goes through
// `AiService.forUser(userId, { orgId, feature: 'localai_music' }).respond(...)`, never an SDK. The feature is
// registered with the AI platform. `NoMusicAi` (no AI slice) is always off.
import type { ModuleRef } from '@nestjs/core';
import { AiFeaturesService, getAiFeature, type AiConfigService, type AiService } from '@marinoscar/platform-api/ai';

import { MUSIC_AI_FEATURE, MusicAiUnavailable, NoMusicAi } from './music-ai';
import { AiServiceMusicAi } from './music-ai.adapter';

const view = (id: string, usable: boolean) => ({ id, label: id, group: null, needs: ['responses'], inputModalities: [], providers: null, requiresHostedTools: [], defaultEffort: null, usable });
const caller = { userId: 'u1', orgId: 'org-1' };

function adapter(options: { enabled: boolean; features: unknown[] | null; answer?: string; error?: unknown }) {
  const respond = options.error ? jest.fn().mockRejectedValue(options.error) : jest.fn().mockResolvedValue({ outputText: options.answer ?? '' });
  const forUser = jest.fn().mockReturnValue({ respond });
  const ai = { forUser } as unknown as AiService;
  const resolveForOrg = jest.fn().mockResolvedValue({ enabled: options.enabled });
  const config = { resolveForOrg } as unknown as AiConfigService;
  const features = { listForUser: jest.fn().mockResolvedValue(options.features ?? []) };
  const moduleRef = {
    get: jest.fn((token: unknown) => {
      if (token !== AiFeaturesService || options.features === null) throw new Error('not found');
      return features;
    }),
  } as unknown as ModuleRef;
  return { port: new AiServiceMusicAi(ai, config, moduleRef), forUser, respond, resolveForOrg, features };
}

describe('AiServiceMusicAi', () => {
  it('registers the music feature with the AI platform, with its label', () => {
    expect(getAiFeature(MUSIC_AI_FEATURE)).toMatchObject({ id: 'localai_music', label: 'Music: lyrics and titles', needs: ['responses'] });
  });

  it('reads the AI switch of the caller\'s organization', async () => {
    const off = adapter({ enabled: false, features: [] });
    expect(await off.port.aiEnabled(caller)).toBe(false);
    expect(off.resolveForOrg).toHaveBeenCalledWith('org-1');
    expect(await adapter({ enabled: true, features: [] }).port.aiEnabled({ userId: 'u1' })).toBe(true);
  });

  it('a model resolves only when the localai_music feature is usable for the person', async () => {
    expect(await adapter({ enabled: true, features: [] }).port.modelResolves(caller)).toBe(false);
    expect(await adapter({ enabled: true, features: [view('localai_music', false), view('other', true)] }).port.modelResolves(caller)).toBe(false);
    const on = adapter({ enabled: true, features: [view('localai_music', true)] });
    expect(await on.port.modelResolves(caller)).toBe(true);
    expect(on.features.listForUser).toHaveBeenCalledWith('u1', 'org-1');
    // Without the slice's feature service in the graph, the call itself decides.
    expect(await adapter({ enabled: true, features: null }).port.modelResolves(caller)).toBe(true);
  });

  it('turns "no key or model" and "AI off" errors into MusicAiUnavailable, and passes other errors on', async () => {
    const coded = (code: string) => Object.assign(new Error('x'), { code });
    const ask = { instructions: '', input: '', maxOutputTokens: 1 };
    await expect(adapter({ enabled: true, features: [], error: coded('AI_KEY_REQUIRED') }).port.ask(caller, ask)).rejects.toMatchObject({ reason: 'no_model' });
    await expect(adapter({ enabled: true, features: [], error: coded('AI_DISABLED') }).port.ask(caller, ask)).rejects.toMatchObject({ reason: 'ai_off' });
    await expect(adapter({ enabled: true, features: [], error: coded('AI_PROVIDER_ERROR') }).port.ask(caller, ask)).rejects.toMatchObject({ code: 'AI_PROVIDER_ERROR' });
  });

  it('asks through AiService.forUser with the organization and the feature id, and returns the text', async () => {
    const { port, forUser, respond } = adapter({ enabled: true, features: [], answer: 'Harbour Lights' });
    await expect(port.ask(caller, { instructions: 'name it', input: 'text', maxOutputTokens: 40 })).resolves.toBe('Harbour Lights');
    expect(forUser).toHaveBeenCalledWith('u1', { feature: 'localai_music', orgId: 'org-1' });
    expect(respond).toHaveBeenCalledWith({ instructions: 'name it', input: 'text', maxOutputTokens: 40 });
    await port.ask({ userId: 'u2' }, { instructions: 'x', input: 'y', maxOutputTokens: 1 });
    expect(forUser).toHaveBeenLastCalledWith('u2', { feature: 'localai_music' });
  });
});

describe('NoMusicAi', () => {
  it('is always off and refuses every call', async () => {
    const port = new NoMusicAi();
    expect(await port.aiEnabled()).toBe(false);
    expect(await port.modelResolves()).toBe(false);
    await expect(port.ask()).rejects.toBeInstanceOf(MusicAiUnavailable);
  });
});
