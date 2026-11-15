// The music feature's port to the cloud's AI (#568, #570): "Suggest a title", "Write lyrics now" and the Automatic
// lyrics of "Make song". The AI platform rules hold: the call is server-side only, through `AiService.forUser(...)`
// (`music-ai.adapter.ts`, loaded only while the `ai` slice is mounted), so no provider SDK is imported here and no key
// ever leaves the server. Without the slice the port is `NoMusicAi`: AI is reported off and every call is refused, so
// the title falls back to the generated one and empty Automatic lyrics make a song with no vocals.
//
// Music AI is available only when ALL of these hold, checked in this order (the first "no" is the reason the page
// shows, with its fix):
//   1. the AI slice's own switch is on, for the person's organization   -> else `ai_off`
//   2. "Use AI for music" (`localaiMusic.aiEnabled`) is on               -> else `music_ai_off`
//   3. a model that writes text resolves for the person                  -> else `no_model`
//
// Nothing here logs the description, the lyrics or the model's answer: only ids, lengths and error names.

/** Injection token of the {@link MusicAiPort}. */
export const MUSIC_AI = Symbol('MUSIC_AI');

/** Injection token of the {@link MusicAiSwitch} ("Use AI for music"). */
export const MUSIC_AI_SWITCH = Symbol('MUSIC_AI_SWITCH');

/** The `AiService` feature id of the music calls (usage rows and the feature's model fit name it). */
export const MUSIC_AI_FEATURE = 'localai_music';

/** Why music AI is not available. */
export type MusicAiReason = 'ai_off' | 'music_ai_off' | 'no_model';

/** The fix for each reason, as the API states it (the page shows the same in English or Spanish). */
export const MUSIC_AI_FIXES: Readonly<Record<MusicAiReason, string>> = {
  ai_off: 'Turn on AI in Settings → AI.',
  music_ai_off: 'Turn on Use AI for music in Settings → AI.',
  no_model: 'Add a provider and model in Settings → AI.',
};

/** `GET /api/localai/music/assist`'s `ai`: enabled, or the reason and its fix. */
export type MusicAiAvailability = { enabled: true; reason: null; fix: null } | { enabled: false; reason: MusicAiReason; fix: string };

export const musicAiOn = (): MusicAiAvailability => ({ enabled: true, reason: null, fix: null });
export const musicAiOff = (reason: MusicAiReason): MusicAiAvailability => ({ enabled: false, reason, fix: MUSIC_AI_FIXES[reason] });

/** Who asks: the user and the organization the call runs in (its AI policy, caps and own key apply). */
export interface MusicAiCaller {
  userId: string;
  orgId?: string | null;
}

/** One short text answer: the instructions and the person's text. */
export interface MusicAiAsk {
  instructions: string;
  input: string;
  maxOutputTokens: number;
}

export interface MusicAiPort {
  /** The AI slice's switch, for this organization (the deployment's, tightened by the organization's). */
  aiEnabled(caller: MusicAiCaller): Promise<boolean>;
  /** A model that writes text (fits the `localai_music` feature) resolves for this person. */
  modelResolves(caller: MusicAiCaller): Promise<boolean>;
  /** One answer's text. Throws on any AI error (the caller decides what the person sees). */
  ask(caller: MusicAiCaller, ask: MusicAiAsk): Promise<string>;
}

/** "Use AI for music": the `localaiMusic` namespace, through its org layer. */
export interface MusicAiSwitch {
  isOn(orgId?: string | null): Promise<boolean>;
}

/** Thrown by {@link MusicAiPort.ask} when the call cannot be made (the AI is off or no model or key resolves). */
export class MusicAiUnavailable extends Error {
  constructor(readonly reason: MusicAiReason) {
    super(`AI is not available (${reason})`);
    this.name = 'MusicAiUnavailable';
  }
}

/** The port while the `ai` slice is not mounted. */
export class NoMusicAi implements MusicAiPort {
  async aiEnabled(): Promise<boolean> {
    return false;
  }

  async modelResolves(): Promise<boolean> {
    return false;
  }

  async ask(): Promise<string> {
    throw new MusicAiUnavailable('ai_off');
  }
}
