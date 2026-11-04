import { PublishResultSchema, UnpublishResultSchema } from './vps-result.schema';

const publish = (over: Record<string, unknown> = {}) => ({
  host: 'ai-n3-staging.marin.cr',
  steps: [{ name: 'dns_upsert', status: 'done', detail: 'ok' }],
  certificateExpiresAt: '2027-02-01T00:00:00.000Z',
  ...over,
});

describe('PublishResultSchema', () => {
  it('accepts a valid result, a null expiry and an offset timestamp', () => {
    expect(PublishResultSchema.safeParse(publish()).success).toBe(true);
    expect(PublishResultSchema.safeParse(publish({ certificateExpiresAt: null })).success).toBe(true);
    expect(PublishResultSchema.safeParse(publish({ certificateExpiresAt: '2027-02-01T00:00:00+02:00' })).success).toBe(true);
  });

  it.each([
    ['an unknown top-level key', publish({ extra: 1 })],
    ['a host outside the pattern', publish({ host: 'evil.example.test' })],
    ['an unknown step name', publish({ steps: [{ name: 'rm_rf', status: 'done', detail: 'x' }] })],
    ['an unpublish step name', publish({ steps: [{ name: 'dns_delete', status: 'done', detail: 'x' }] })],
    ['an unknown status', publish({ steps: [{ name: 'dns_upsert', status: 'maybe', detail: 'x' }] })],
    ['an extra key on a step', publish({ steps: [{ name: 'dns_upsert', status: 'done', detail: 'x', secret: 'y' }] })],
    ['a detail over 500 characters', publish({ steps: [{ name: 'dns_upsert', status: 'done', detail: 'x'.repeat(501) }] })],
    ['more than 10 steps', publish({ steps: Array.from({ length: 11 }, () => ({ name: 'dns_upsert', status: 'done', detail: 'x' })) })],
    ['a date without a time', publish({ certificateExpiresAt: '2027-02-01' })],
    ['a missing expiry', { host: 'ai-n3.marin.cr', steps: [] }],
  ])('refuses %s', (_label, value) => expect(PublishResultSchema.safeParse(value).success).toBe(false));
});

describe('UnpublishResultSchema', () => {
  it('accepts its own step names and nothing else', () => {
    const ok = { host: 'ai-n3.marin.cr', steps: [{ name: 'vhost_remove', status: 'skipped', detail: 'x' }, { name: 'certificate_kept', status: 'skipped', detail: 'x' }] };
    expect(UnpublishResultSchema.safeParse(ok).success).toBe(true);
    expect(UnpublishResultSchema.safeParse({ ...ok, steps: [{ name: 'dns_upsert', status: 'done', detail: 'x' }] }).success).toBe(false);
    expect(UnpublishResultSchema.safeParse({ ...ok, certificateExpiresAt: null }).success).toBe(false);
  });
});
