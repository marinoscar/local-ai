import { ApiError, apiFetch } from "../api";
import type { TtsDeps } from "./ttsQueue";

// The browser side of the speak pipeline: the TTS request and one HTMLAudioElement for playback.

export interface Voice {
  id: string;
  language: string;
  display_name: string;
  default?: boolean;
}

/** listVoices is GET /api/v1/speech/voices; a failure is an empty list (the screen then shows no picker). */
export async function listVoices(): Promise<Voice[]> {
  try {
    const res = await apiFetch("/api/v1/speech/voices");
    const body = (await res.json()) as { voices?: Voice[] };
    return body.voices ?? [];
  } catch {
    return [];
  }
}

export const VOICE_KEY_PREFIX = "lai.portal.voice.";

export function storedVoice(lang: string): string | null {
  try {
    return window.localStorage.getItem(VOICE_KEY_PREFIX + lang);
  } catch {
    return null;
  }
}

export function storeVoice(lang: string, id: string): void {
  try {
    if (id) window.localStorage.setItem(VOICE_KEY_PREFIX + lang, id);
    else window.localStorage.removeItem(VOICE_KEY_PREFIX + lang);
  } catch {
    // Storage blocked: the choice lasts until reload.
  }
}

/** pickVoice is the voice id to send for a language: the remembered one if still installed, else null (the agent picks). */
export function pickVoice(voices: Voice[], lang: string): string | null {
  const id = storedVoice(lang);
  return id && voices.some((v) => v.id === id && v.language === lang)
    ? id
    : null;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("aborted", "AbortError"));
      return;
    }
    const t = window.setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        window.clearTimeout(t);
        reject(new DOMException("aborted", "AbortError"));
      },
      { once: true },
    );
  });
}

/** splitInHalf cuts text at the space nearest its middle (a text too long for one request, LAI-SVC-024). */
export function splitInHalf(text: string): [string, string] | null {
  const mid = Math.floor(text.length / 2);
  const left = text.lastIndexOf(" ", mid);
  const right = text.indexOf(" ", mid);
  const cut = left > 0 ? left : right;
  if (cut <= 0) return null;
  return [text.slice(0, cut).trim(), text.slice(cut).trim()];
}

export interface SpeakOptions {
  language: string;
  voice: string | null;
}

/**
 * fetchSpeech is POST /api/v1/speech/tts for one sentence. 503 LAI-SVC-022 (no voice) gives null;
 * 413 LAI-SVC-024 splits the text and asks twice; 429 LAI-SVC-015 waits a second and asks again
 * (three times at most).
 */
export function makeFetchSpeech(
  o: () => SpeakOptions,
): TtsDeps["fetchWav"] {
  const one = async (
    text: string,
    signal: AbortSignal,
    depth: number,
  ): Promise<Blob[] | null> => {
    for (let attempt = 0; ; attempt++) {
      try {
        const res = await apiFetch("/api/v1/speech/tts", {
          method: "POST",
          headers: { "Content-Type": "application/json", Accept: "audio/wav" },
          body: JSON.stringify({
            text,
            voice: o().voice,
            language: o().language,
            speed: 1.0,
          }),
          signal,
        });
        return [await res.blob()];
      } catch (e) {
        if (!(e instanceof ApiError)) throw e;
        if (e.code === "LAI-SVC-022") return null;
        if ((e.status === 413 || e.code === "LAI-SVC-024") && depth < 4) {
          const halves = splitInHalf(text);
          if (!halves) throw e;
          const out: Blob[] = [];
          for (const h of halves) {
            const part = await one(h, signal, depth + 1);
            if (part === null) return null;
            out.push(...part);
          }
          return out;
        }
        if ((e.status === 429 || e.code === "LAI-SVC-015") && attempt < 3) {
          await sleep(1000, signal);
          continue;
        }
        throw e;
      }
    }
  };
  return (text, signal) => one(text, signal, 0);
}

/** One HTMLAudioElement plays every WAV, one after the other (the queue calls play in order). */
export function makeAudioPlayer(): TtsDeps["play"] {
  const audio = new Audio();
  return (wav, signal) =>
    new Promise<void>((resolve) => {
      const url = URL.createObjectURL(wav);
      const finish = () => {
        audio.removeEventListener("ended", finish);
        audio.removeEventListener("error", finish);
        signal.removeEventListener("abort", onAbort);
        audio.pause();
        URL.revokeObjectURL(url);
        resolve();
      };
      const onAbort = () => finish();
      if (signal.aborted) {
        finish();
        return;
      }
      audio.addEventListener("ended", finish);
      audio.addEventListener("error", finish);
      signal.addEventListener("abort", onAbort);
      audio.src = url;
      audio.play().catch(finish);
    });
}
