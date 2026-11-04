import { buildChatBody, type ChatMessage } from "../chat/stream";

// The conversation about one photo (survival-portal.md section 4.6 steps 4 and 5; the message
// format is companion-services.md section 4.5): the image travels once, in the first user message;
// follow-up turns are text only; at most MAX_TURNS questions, then the person starts over.

export const MAX_TURNS = 4;

export interface Turn {
  question: string;
  answer: string;
}

export interface PhotoRequest {
  model: string;
  system: string;
  /** Answered turns so far, oldest first (at most MAX_TURNS - 1 when a new question is asked). */
  turns: Turn[];
  question: string;
  /** data:image/jpeg;base64,... of the prepared photo. */
  imageUrl: string;
}

function firstUser(question: string, imageUrl: string): ChatMessage {
  return {
    role: "user",
    content: [
      { type: "text", text: question },
      { type: "image_url", image_url: { url: imageUrl } },
    ],
  };
}

/** photoBody is the request body of POST /chat/v1/chat/completions for a question about the photo. */
export function photoBody(r: PhotoRequest): Record<string, unknown> {
  const opts = {
    model: r.model,
    system: r.system,
    temperature: 0.2,
    maxTokens: 1024,
  };
  if (r.turns.length === 0) {
    return buildChatBody({ ...opts, user: firstUser(r.question, r.imageUrl).content });
  }
  const history: ChatMessage[] = [];
  r.turns.forEach((t, i) => {
    history.push(
      i === 0 ? firstUser(t.question, r.imageUrl) : { role: "user", content: t.question },
      { role: "assistant", content: t.answer },
    );
  });
  return buildChatBody({ ...opts, history, user: r.question });
}

/** canAsk: another question fits in the conversation. */
export function canAsk(turns: Turn[]): boolean {
  return turns.length < MAX_TURNS;
}
