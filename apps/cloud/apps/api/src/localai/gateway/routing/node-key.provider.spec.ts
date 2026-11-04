import { Logger } from '@nestjs/common';

import { LOCALAI_NODE_KEY_REPORTED_EVENT } from '../../keys/node-key.events';
import { NODE_KEY_CACHE_TTL_MS, NodeKeyService, NodeKeyUnreadableError } from '../../keys/node-key.service';
import { FakeBus } from './routing.fixtures';
import { GatewayNodeKeyError, NodeKeyProvider } from './node-key.provider';

// A key-shaped value built at run time, so no literal looks like a credential.
const keyValue = (): string => `k${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;

describe('NodeKeyProvider over NodeKeyService', () => {
  beforeAll(() => {
    jest.spyOn(Logger.prototype, 'error').mockImplementation(() => undefined);
  });
  afterAll(() => jest.restoreAllMocks());

  function setup() {
    const credentials = { getSecret: jest.fn<Promise<string | null>, [string, string]>() };
    const bus = new FakeBus();
    const service = new NodeKeyService({} as never, credentials as never, {} as never, bus as never);
    service.onModuleInit();
    return { credentials, bus, service, provider: new NodeKeyProvider(service) };
  }

  it('returns the key of the node from the credential store at the documented address', async () => {
    const { credentials, provider } = setup();
    const key = keyValue();
    credentials.getSecret.mockResolvedValue(key);
    await expect(provider.get('ai-n1')).resolves.toBe(key);
    expect(credentials.getSecret).toHaveBeenCalledWith('localai_node_llama_key', 'ai-n1');
  });

  it('returns null when no key is stored', async () => {
    const { credentials, provider } = setup();
    credentials.getSecret.mockResolvedValue(null);
    await expect(provider.get('ai-n1')).resolves.toBeNull();
  });

  it('reads the store once within the cache window and again after 60 s', async () => {
    const { credentials, provider } = setup();
    credentials.getSecret.mockResolvedValue(keyValue());
    const now = jest.spyOn(Date, 'now');
    const start = 1_700_000_000_000;
    now.mockReturnValue(start);
    await provider.get('ai-n1');
    now.mockReturnValue(start + NODE_KEY_CACHE_TTL_MS - 1);
    await provider.get('ai-n1');
    expect(credentials.getSecret).toHaveBeenCalledTimes(1);
    now.mockReturnValue(start + NODE_KEY_CACHE_TTL_MS + 1);
    await provider.get('ai-n1');
    expect(credentials.getSecret).toHaveBeenCalledTimes(2);
    now.mockRestore();
  });

  it('the cache lasts at most 60 s', () => {
    expect(NODE_KEY_CACHE_TTL_MS).toBeLessThanOrEqual(60_000);
  });

  it('drops the cached key when localai.node.key_reported arrives', async () => {
    const { credentials, bus, provider } = setup();
    const first = keyValue();
    const second = keyValue();
    credentials.getSecret.mockResolvedValueOnce(first).mockResolvedValueOnce(second);
    await expect(provider.get('ai-n1')).resolves.toBe(first);
    bus.emit(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: 'ai-n1', keyHint: 'aaaa…bbbb', reason: 'rotate' });
    await expect(provider.get('ai-n1')).resolves.toBe(second);
  });

  it('keeps the cached key of another node when one is reported', async () => {
    const { credentials, bus, provider } = setup();
    credentials.getSecret.mockResolvedValue(keyValue());
    await provider.get('ai-n1');
    bus.emit(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: 'ai-n2', keyHint: 'aaaa…bbbb', reason: 'rotate' });
    await provider.get('ai-n1');
    expect(credentials.getSecret).toHaveBeenCalledTimes(1);
  });

  it('maps a decrypt failure to the typed LAI-GW-050 error, without the cause', async () => {
    const { credentials, provider } = setup();
    credentials.getSecret.mockRejectedValue(new Error('bad decrypt: secret-material-in-message'));
    const error = await provider.get('ai-n1').catch((e: unknown) => e);
    expect(error).toBeInstanceOf(GatewayNodeKeyError);
    expect(error).toMatchObject({ code: 'LAI-GW-050', status: 503, nodeId: 'ai-n1' });
    expect(JSON.stringify(error)).not.toContain('secret-material');
    expect((error as Error).message).not.toContain('secret-material');
  });

  it('maps the service error itself to the same typed error, and caches nothing after a failure', async () => {
    const { credentials, provider } = setup();
    credentials.getSecret.mockRejectedValueOnce(new Error('x'));
    await expect(provider.get('ai-n1')).rejects.toBeInstanceOf(GatewayNodeKeyError);
    const key = keyValue();
    credentials.getSecret.mockResolvedValueOnce(key);
    await expect(provider.get('ai-n1')).resolves.toBe(key);
    expect(new NodeKeyUnreadableError('ai-n1')).toBeInstanceOf(Error);
  });
});
