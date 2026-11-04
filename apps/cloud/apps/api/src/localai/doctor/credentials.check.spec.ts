import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { expectContract, expectNoSecretShapes, leakBait } from '../../../test/localai/support/doctor-asserts';
import { CredentialsDoctorCheck, PlatformCipherProbe, type CipherProbe } from './credentials.check';

const registry = { register: jest.fn() };

function build(options: { names?: string[]; getSecret?: jest.Mock; list?: jest.Mock; cipher?: CipherProbe } = {}) {
  const getSecret = options.getSecret ?? jest.fn(async () => 'plaintext-never-looked-at');
  const list = options.list ?? jest.fn(async () => (options.names ?? []).map((name) => ({ name })));
  const cipher = options.cipher ?? { roundTrip: jest.fn() };
  const check = new CredentialsDoctorCheck(registry as never, { list, getSecret } as never, cipher);
  return { check, getSecret, list, cipher };
}

describe('localai.credentials', () => {
  it('registers itself under its id', () => {
    const { check } = build();
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
    expect(check.id).toBe('localai.credentials');
  });

  it('passes when a stored key reads back, and asks for exactly one', async () => {
    const { check, getSecret, cipher } = build({ names: ['ai-n1', 'ai-n2'] });
    const outcome = await check.run();
    expect(outcome.status).toBe('pass');
    expect(getSecret).toHaveBeenCalledTimes(1);
    expect(getSecret).toHaveBeenCalledWith('localai_node_llama_key', 'ai-n1');
    expect(cipher.roundTrip).not.toHaveBeenCalled();
    expectContract(outcome);
  });

  it('never puts the decrypted value in the outcome', async () => {
    const bait = leakBait();
    const { check } = build({ names: ['ai-n1'], getSecret: jest.fn(async () => bait.hex64) });
    expect(JSON.stringify(await check.run())).not.toContain(bait.hex64);
  });

  it('passes with no stored key when the in-memory round trip works, and reads nothing', async () => {
    const { check, getSecret, cipher } = build();
    const outcome = await check.run();
    expect(outcome.status).toBe('pass');
    expect(cipher.roundTrip).toHaveBeenCalledWith('localai_node_llama_key');
    expect(getSecret).not.toHaveBeenCalled();
  });

  it('fails with LAI-CLD-032 when a stored key cannot be decrypted (wrong key)', async () => {
    const { check } = build({ names: ['ai-n1'], getSecret: jest.fn(async () => { throw new Error('Unsupported state or unable to authenticate data'); }) });
    const outcome = await check.run();
    expect(outcome.status).toBe('fail');
    expect(outcome.data?.code).toBe('LAI-CLD-032');
    expect(outcome.detail).toContain('LAI-CLD-032');
    expect(outcome.remedy).toContain('SECRETS_ENCRYPTION_KEY');
    // The error NAME is reported, not its message.
    expect(outcome.error).toBe('Error');
    expect(JSON.stringify(outcome)).not.toContain('authenticate');
    expectContract(outcome);
  });

  it('fails with LAI-CLD-032 when nothing is stored and the cipher cannot work (key missing)', async () => {
    const { check } = build({ cipher: { roundTrip: () => { throw new Error('SECRETS_ENCRYPTION_KEY is not set'); } } });
    const outcome = await check.run();
    expect(outcome.status).toBe('fail');
    expect(outcome.data?.code).toBe('LAI-CLD-032');
    expectContract(outcome);
  });

  it('fails (and does not throw) when the credential list itself cannot be read', async () => {
    const { check } = build({ list: jest.fn(async () => { throw new Error('connection refused'); }) });
    expect((await check.run()).status).toBe('fail');
  });

  it('points at the re-send keys action in the remedy', async () => {
    const { check } = build({ cipher: { roundTrip: () => { throw new Error('x'); } } });
    expect((await check.run()).remedy).toContain('POST /api/localai/nodes/resend-keys');
  });
});

describe('PlatformCipherProbe', () => {
  it('round-trips with the configured key', () => {
    // The real cipher reads SECRETS_ENCRYPTION_KEY once per process; the app tests set it. Skip when this run has none.
    if (!process.env.SECRETS_ENCRYPTION_KEY) return;
    expect(() => new PlatformCipherProbe().roundTrip('localai_node_llama_key')).not.toThrow();
  });
});

describeDoctorCheckConformance(
  'localai.credentials',
  () => new CredentialsDoctorCheck(registry as never, { list: async () => [{ name: 'ai-n1' }], getSecret: async () => 'x' } as never, { roundTrip: () => undefined }),
  {
    scenarios: [
      { name: 'a stored key reads back', expectStatus: 'pass' },
      {
        name: 'the key is wrong',
        build: () => new CredentialsDoctorCheck(registry as never, { list: async () => [{ name: 'ai-n1' }], getSecret: async () => { throw new Error('bad decrypt'); } } as never, { roundTrip: () => undefined }),
        expectStatus: 'fail',
      },
    ],
  },
);

it('keeps expectNoSecretShapes honest (the helper catches what it claims to)', () => {
  const bait = leakBait();
  expect(() => expectNoSecretShapes(`x ${bait.hex64}`)).toThrow();
  expect(() => expectNoSecretShapes(`x ${bait.token}`)).toThrow();
  expect(() => expectNoSecretShapes(`x ${bait.ip}`)).toThrow();
});
