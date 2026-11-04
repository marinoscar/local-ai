import { Readable } from 'node:stream';
import Fastify from 'fastify';

import { LOCALAI_STATE_BODY_LIMIT_BYTES } from '../contracts/state.schema';
import { nodeLinkBodyLimitFor, registerNodeLinkBodyLimits } from './node-link.body-limits';

describe('nodeLinkBodyLimitFor', () => {
  it.each<[string, string, number | null]>([
    ['POST', '/api/nodes/:id/localai/state', LOCALAI_STATE_BODY_LIMIT_BYTES],
    ['post', '/nodes/:id/localai/state', LOCALAI_STATE_BODY_LIMIT_BYTES],
    ['GET', '/api/nodes/:id/localai/state', null],
    ['POST', '/api/nodes/:id/localai/config', null],
    ['POST', '/api/nodes/:id/localai/state/extra', null],
    ['POST', '/api/other/state', null],
  ])('%s %s -> %s', (method, url, expected) => {
    expect(nodeLinkBodyLimitFor(method, url)).toBe(expected);
  });
});

describe('registerNodeLinkBodyLimits on a real Fastify instance', () => {
  it('limits only the state route: 413 over 256 KiB, while another route keeps the default', async () => {
    const fastify = Fastify();
    registerNodeLinkBodyLimits(fastify);
    fastify.post('/api/nodes/:id/localai/state', async () => ({ ok: true }));
    fastify.post('/api/other', async () => ({ ok: true }));
    const body = JSON.stringify({ padding: 'x'.repeat(LOCALAI_STATE_BODY_LIMIT_BYTES) });
    const headers = { 'content-type': 'application/json' };
    expect((await fastify.inject({ method: 'POST', url: '/api/nodes/abc/localai/state', headers, payload: body })).statusCode).toBe(413);
    expect((await fastify.inject({ method: 'POST', url: '/api/nodes/abc/localai/state', headers, payload: '{"a":1}' })).statusCode).toBe(200);
    expect((await fastify.inject({ method: 'POST', url: '/api/other', headers, payload: body })).statusCode).toBe(200);
    await fastify.close();
  });

  it('also stops a chunked body that has no content-length', async () => {
    const fastify = Fastify();
    registerNodeLinkBodyLimits(fastify);
    fastify.post('/api/nodes/:id/localai/state', async () => ({ ok: true }));
    const chunk = Buffer.from(`"${'y'.repeat(64 * 1024)}"`);
    const stream = Readable.from([Buffer.from('['), chunk, Buffer.from(','), chunk, Buffer.from(','), chunk, Buffer.from(','), chunk, Buffer.from(','), chunk, Buffer.from(']')]);
    const response = await fastify.inject({ method: 'POST', url: '/api/nodes/abc/localai/state', headers: { 'content-type': 'application/json' }, payload: stream });
    expect(response.statusCode).toBe(413);
    await fastify.close();
  });
});
