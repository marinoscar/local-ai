import { FakeBus } from '../routing/routing.fixtures';
import { GATEWAY_POLICY_UPDATED_CHANNEL } from './policy-events';
import { PolicyCacheListener } from './policy-cache.listener';

describe('PolicyCacheListener', () => {
  it('drops the cached directory on a policy event, and stops after the module is destroyed', () => {
    const bus = new FakeBus();
    const directory = { invalidate: jest.fn() };
    const listener = new PolicyCacheListener(bus as never, directory as never);
    listener.onModuleInit();
    bus.emit(GATEWAY_POLICY_UPDATED_CHANNEL, { modelId: 'model-a' });
    expect(directory.invalidate).toHaveBeenCalledTimes(1);
    listener.onModuleDestroy();
    bus.emit(GATEWAY_POLICY_UPDATED_CHANNEL, { modelId: 'model-a' });
    expect(directory.invalidate).toHaveBeenCalledTimes(1);
  });
});
