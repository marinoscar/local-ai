// The music feature's port to the cloud's AI (#568): "Suggest a title" and "Write lyrics for me". The AI platform
// rules hold: the call is server-side only, through `AiService.forUser(...)` (`music-ai.adapter.ts`, loaded only
// while the `ai` slice is mounted), so no provider SDK is imported here and no key ever leaves the server. Without the
// slice the port is `NoMusicAi`: AI is reported off and every call is refused, so the title falls back to the
// generated one and the lyrics choice is disabled on the page.
//
// Nothing here logs the description, the lyrics or the model's answer: only ids, lengths and error names.

/** Injection token of the {@link MusicAiPort}. */
export const MUSIC_AI = Symbol('MUSIC_AI');

/** The `AiService` feature id of the music calls (usage rows and the feature's model policy name it). */
export const MUSIC_AI_FEATURE = 'localai_music';

/** Whether this person can use the cloud's AI now, and if not, why (the page shows the reason and a fix). */
export type MusicAiAvailability =
  | { available: true; reason: null }
  | { available: false; reason: 'ai_off' | 'no_model' };

/** One short text answer: the instructions and the person's text. */
export interface MusicAiAsk {
  instructions: string;
  input: string;
  maxOutputTokens: number;
}

export interface MusicAiPort {
  /** AI is enabled for the deployment and a model that answers text resolves for this user. */
  availability(userId: string): Promise<MusicAiAvailability>;
  /** One answer's text. Throws on any AI error (the caller decides what the person sees). */
  ask(userId: string, ask: MusicAiAsk): Promise<string>;
}

/** Thrown by {@link NoMusicAi.ask}: the AI slice is not mounted. */
export class MusicAiUnavailable extends Error {
  constructor(readonly reason: 'ai_off' | 'no_model') {
    super(`AI is not available (${reason})`);
    this.name = 'MusicAiUnavailable';
  }
}

/** The port while the `ai` slice is not mounted (this deployment's default). */
export class NoMusicAi implements MusicAiPort {
  async availability(): Promise<MusicAiAvailability> {
    return { available: false, reason: 'ai_off' };
  }

  async ask(): Promise<string> {
    throw new MusicAiUnavailable('ai_off');
  }
}
