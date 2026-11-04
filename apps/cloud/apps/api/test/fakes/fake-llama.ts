// A fake llama-server for the gateway specs: a REAL local HTTP server (no mocks of the client), so the upstream client,
// its timeouts and its socket handling are exercised for real. It records every request it receives (headers and body),
// answers according to a replaceable behaviour, and counts the connections the gateway closed on it while a request was
// still open (the proof that a client abort reaches the node).
import { createServer, type IncomingHttpHeaders, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';

export interface FakeLlamaRequest {
  method: string;
  url: string;
  headers: IncomingHttpHeaders;
  body: string;
}

export type FakeLlamaBehaviour = (request: FakeLlamaRequest, response: ServerResponse, raw: IncomingMessage) => void | Promise<void>;

/** A llama-server answer: JSON with the given status. */
export function respondJson(response: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void {
  const payload = typeof body === 'string' ? body : JSON.stringify(body);
  response.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'content-length': Buffer.byteLength(payload), ...headers });
  response.end(payload);
}

/** A well-formed OK answer for the three endpoints, echoing the model it was asked for. */
export const okBehaviour: FakeLlamaBehaviour = (request, response) => {
  const body = JSON.parse(request.body) as { model?: string };
  if (request.url === '/v1/embeddings') {
    respondJson(response, 200, { object: 'list', model: body.model, data: [{ object: 'embedding', index: 0, embedding: [0.1, 0.2] }], usage: { prompt_tokens: 7, total_tokens: 7 } });
  } else if (request.url === '/v1/completions') {
    respondJson(response, 200, { id: 'cmpl-1', object: 'text_completion', model: body.model, choices: [{ index: 0, text: 'hi', finish_reason: 'stop' }], usage: { prompt_tokens: 5, completion_tokens: 3, total_tokens: 8 } });
  } else {
    respondJson(response, 200, { id: 'chatcmpl-1', object: 'chat.completion', model: body.model, choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: 'stop' }], usage: { prompt_tokens: 11, completion_tokens: 4, total_tokens: 15 } });
  }
};

export class FakeLlama {
  readonly requests: FakeLlamaRequest[] = [];
  /** Milliseconds (since the epoch) at which a request was closed by the CLIENT before the node answered. */
  readonly abortedAt: number[] = [];
  behaviour: FakeLlamaBehaviour = okBehaviour;

  private constructor(private readonly server: Server) {}

  static async start(behaviour: FakeLlamaBehaviour = okBehaviour): Promise<FakeLlama> {
    const holder: { fake?: FakeLlama } = {};
    const server = createServer((raw, response) => {
      const chunks: Buffer[] = [];
      raw.on('data', (chunk: Buffer) => chunks.push(chunk));
      raw.on('end', () => {
        const fake = holder.fake!;
        const request: FakeLlamaRequest = { method: raw.method ?? '', url: raw.url ?? '', headers: raw.headers, body: Buffer.concat(chunks).toString('utf8') };
        fake.requests.push(request);
        response.on('close', () => {
          if (!response.writableFinished) fake.abortedAt.push(Date.now());
        });
        void Promise.resolve(fake.behaviour(request, response, raw)).catch(() => response.destroy());
      });
    });
    const fake = new FakeLlama(server);
    holder.fake = fake;
    fake.behaviour = behaviour;
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    return fake;
  }

  get origin(): string {
    return `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
  }

  async close(): Promise<void> {
    this.server.closeAllConnections();
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
  }
}

/** An origin on which nothing listens (connection refused), for the "node unreachable" cases. */
export async function closedOrigin(): Promise<string> {
  const fake = await FakeLlama.start();
  const origin = fake.origin;
  await fake.close();
  return origin;
}
