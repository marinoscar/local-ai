// The claim-time params seam (E11): a registered hook finishes a type's params at the claim; a type without one, and a
// hook that fails, leave the stored params as they are; a type has at most one hook.
import { ClaimParamsHooks } from './claim-params.hooks';

describe('ClaimParamsHooks', () => {
  it('passes the params of a type without a hook through unchanged', async () => {
    const hooks = new ClaimParamsHooks();
    const params = { model_id: 'qwen3.6-35b-a3b-q4km' };
    await expect(hooks.apply('localai.model.load', params, 'c1')).resolves.toBe(params);
  });

  it('applies the hook of its type, with the command id', async () => {
    const hooks = new ClaimParamsHooks();
    const hook = jest.fn(async (params: Record<string, unknown>) => ({ ...params, url: 'https://signed.example.test/x' }));
    hooks.register('localai.music.generate', hook);
    await expect(hooks.apply('localai.music.generate', { request_id: 'r' }, 'c1')).resolves.toEqual({ request_id: 'r', url: 'https://signed.example.test/x' });
    expect(hook).toHaveBeenCalledWith({ request_id: 'r' }, 'c1');
  });

  it('keeps the stored params when the hook fails, and never throws', async () => {
    const hooks = new ClaimParamsHooks();
    hooks.register('localai.music.generate', async () => {
      throw new Error('storage down');
    });
    const params = { request_id: 'r' };
    await expect(hooks.apply('localai.music.generate', params, 'c1')).resolves.toBe(params);
  });

  it('refuses a second hook for the same type', () => {
    const hooks = new ClaimParamsHooks();
    hooks.register('localai.music.generate', async (params) => params);
    expect(() => hooks.register('localai.music.generate', async (params) => params)).toThrow(/already registered/);
  });
});
