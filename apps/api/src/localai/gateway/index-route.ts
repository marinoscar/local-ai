// `GET /v1/` (docs/specs/gateway.md section 3.1 point 6): the small JSON index of the OpenAI surface. No auth.
export const GATEWAY_DOCS_URL = 'https://github.com/marinoscar/local-ai/blob/main/docs/specs/gateway.md';

/** The public path of every endpoint the gateway serves (spec section 3.2). */
export const GATEWAY_ENDPOINTS = [
  { method: 'GET', path: '/v1/models' },
  { method: 'GET', path: '/v1/models/{id}' },
  { method: 'POST', path: '/v1/chat/completions' },
  { method: 'POST', path: '/v1/completions' },
  { method: 'POST', path: '/v1/embeddings' },
  { method: 'POST', path: '/v1/audio/transcriptions' },
] as const;

export function buildGatewayIndex() {
  return { object: 'localai.gateway', endpoints: GATEWAY_ENDPOINTS.map((endpoint) => ({ ...endpoint })), docs: GATEWAY_DOCS_URL };
}
