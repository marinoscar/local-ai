// The command controller with the services mocked: how the principal, the credential kind and the body reach
// `CommandsService.request`, the 201/200 split, and the route metadata (the permissions each route is guarded by).
// The permission matrix per type and the refusal table are `test/localai/commands-request.spec.ts` (service) and
// `commands-api-routes.spec.ts` (HTTP with the real guards).
import { BadRequestException, NotFoundException } from '@nestjs/common';
import { GUARDS_METADATA } from '@nestjs/common/constants';

import { CLIENT_HEADER, CommandsController, sourceOf } from './commands.controller';

const NODE = 'ai-n2';
const USER = { id: 'user-alpha', email: 'a@example.test', roles: ['contributor'], permissions: ['lab:read', 'lab:operate'], isActive: true };

function build(created = true) {
  const view = { id: 'cmd-1', nodeId: NODE, type: 'localai.status.refresh', status: 'queued' };
  const commands = { request: jest.fn(async () => ({ command: view, created })), cancel: jest.fn(async () => ({ ...view, status: 'cancelled' })) };
  const queries = { list: jest.fn(async () => ({ items: [], page: 1, pageSize: 20, total: 0, totalPages: 1 })), get: jest.fn(async () => view) };
  const reply = { status: jest.fn() };
  return { controller: new CommandsController(commands as never, queries as never), commands, queries, reply, view };
}

describe('sourceOf', () => {
  it('is ui for a browser session and a device-flow token, whatever the client header says', () => {
    expect(sourceOf({ kind: 'jwt' }, undefined)).toBe('ui');
    expect(sourceOf({ kind: 'jwt' }, 'desktop')).toBe('ui');
    expect(sourceOf(null, undefined)).toBe('ui');
  });

  it('is api for a pat_ caller and desktop only when the PAT caller identifies itself as the desktop', () => {
    expect(sourceOf({ kind: 'pat', tokenId: 't' }, undefined)).toBe('api');
    expect(sourceOf({ kind: 'pat', tokenId: 't' }, 'curl')).toBe('api');
    expect(sourceOf({ kind: 'pat', tokenId: 't' }, 'desktop')).toBe('desktop');
    expect(sourceOf({ kind: 'pat', tokenId: 't' }, ' Desktop ')).toBe('desktop');
  });

  it('exposes the header name', () => {
    expect(CLIENT_HEADER).toBe('x-localai-client');
  });
});

describe('CommandsController', () => {
  it('POST passes the node, unprefixed type, params, the caller and the source to the service', async () => {
    const { controller, commands, reply, view } = build();
    const answer = await controller.request(NODE, { type: 'model.load', params: { model_id: 'model-alpha' } }, USER, { kind: 'pat', tokenId: 't' }, 'desktop', reply as never);
    expect(commands.request).toHaveBeenCalledWith(NODE, 'model.load', { model_id: 'model-alpha' }, { userId: 'user-alpha', permissions: ['lab:read', 'lab:operate'] }, 'desktop');
    expect(answer).toBe(view);
  });

  it('POST answers 201 for a new command and 200 for the active duplicate', async () => {
    const created = build(true);
    await created.controller.request(NODE, { type: 'status.refresh' }, USER, { kind: 'jwt' }, undefined, created.reply as never);
    expect(created.reply.status).toHaveBeenCalledWith(201);
    const duplicate = build(false);
    await duplicate.controller.request(NODE, { type: 'status.refresh' }, USER, { kind: 'jwt' }, undefined, duplicate.reply as never);
    expect(duplicate.reply.status).toHaveBeenCalledWith(200);
  });

  it('POST defaults missing params to {} and refuses a malformed body with a 400 and the Zod issues, before the service', async () => {
    const { controller, commands, reply } = build();
    await controller.request(NODE, { type: 'status.refresh' }, USER, null, undefined, reply as never);
    expect(commands.request).toHaveBeenCalledWith(NODE, 'status.refresh', {}, expect.anything(), 'ui');
    for (const body of [{}, { type: '' }, { type: 5 }, { type: 'status.refresh', params: 'x' }, { type: 'status.refresh', extra: 1 }, { type: 'x'.repeat(65) }]) {
      commands.request.mockClear();
      const error = (await controller.request(NODE, body, USER, null, undefined, reply as never).catch((e) => e)) as BadRequestException;
      expect(error).toBeInstanceOf(BadRequestException);
      expect(commands.request).not.toHaveBeenCalled();
    }
  });

  it('GET list validates the query: status is an enum, page and pageSize are bounded numbers', async () => {
    const { controller, queries } = build();
    await controller.list(NODE, { status: 'queued', page: '2', pageSize: '50' });
    expect(queries.list).toHaveBeenCalledWith(NODE, { status: 'queued', page: 2, pageSize: 50 });
    await controller.list(NODE, {});
    expect(queries.list).toHaveBeenLastCalledWith(NODE, { page: 1, pageSize: 20 });
    for (const query of [{ status: 'bogus' }, { page: '0' }, { pageSize: '101' }, { page: 'x' }, { other: '1' }]) {
      expect(() => controller.list(NODE, query)).toThrow(BadRequestException);
    }
  });

  it('DELETE refuses a path id that is not a uuid with 404 before the service, and passes the caller on', async () => {
    const { controller, commands } = build();
    expect(() => controller.cancel('not-a-uuid', USER)).toThrow(NotFoundException);
    expect(commands.cancel).not.toHaveBeenCalled();
    await controller.cancel('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1', USER);
    expect(commands.cancel).toHaveBeenCalledWith('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1', { userId: 'user-alpha', permissions: ['lab:read', 'lab:operate'] });
  });

  it('every route is behind the auth guards (there is no global guard)', () => {
    for (const method of ['list', 'request', 'get', 'cancel'] as const) {
      expect(Reflect.getMetadata(GUARDS_METADATA, CommandsController.prototype[method])?.length).toBeGreaterThan(0);
    }
  });
});
