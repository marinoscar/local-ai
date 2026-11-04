import { buildSnapshot } from './build-candidates';
import { model, node, NOW } from './routing.fixtures';

const secondsAgo = (s: number): Date => new Date(NOW.getTime() - s * 1000);
const admitted = (rows: Parameters<typeof buildSnapshot>[0]): string[] => [...buildSnapshot(rows, NOW).routable.keys()];

describe('the gatewayRoutable filter (cloud-app.md section 4.5)', () => {
  it('admits a node that passes every part', () => {
    expect(admitted([node('ai-n1', [model('model-a')])])).toEqual(['ai-n1']);
  });

  const excluded: Array<[string, Parameters<typeof node>[2]]> = [
    ['mode is offline', { mode: 'offline' }],
    ['mode is survival', { mode: 'survival' }],
    ['no snapshot has been received (mode unknown)', { mode: null, lastStateAt: null }],
    ['gatewayEnabled is false', { gatewayEnabled: false }],
    ['phase is pending', { phase: 'pending' }],
    ['servePattern is none', { servePattern: 'none' }],
    ['no key was reported', { keyReportedAt: null }],
    ['the snapshot is 121 s old', { lastStateAt: secondsAgo(121) }],
    ['the snapshot is 10 minutes old', { lastStateAt: secondsAgo(600) }],
    ['EAB says stale', { eab: 'stale' }],
    ['EAB says offline', { eab: 'offline' }],
  ];
  it.each(excluded)('excludes a node when %s', (_name, overrides) => {
    expect(admitted([node('ai-n1', [model('model-a')], overrides)])).toEqual([]);
  });

  it('keeps a snapshot of exactly 120 s (the boundary is inclusive)', () => {
    expect(admitted([node('ai-n1', [model('model-a')], { lastStateAt: secondsAgo(120) })])).toEqual(['ai-n1']);
  });

  it('never admits a retired node, and does not count its models as present', () => {
    const snapshot = buildSnapshot([node('ai-n1', [model('model-a')], { phase: 'retired' })], NOW);
    expect(snapshot.routable.size).toBe(0);
    expect(snapshot.present.has('model-a')).toBe(false);
  });

  it('admits only the routable nodes of a mixed fleet', () => {
    const rows = [node('ai-n1', [model('model-a')]), node('ai-n2', [model('model-a')], { mode: 'offline' }), node('ai-n3', [model('model-a')], { keyReportedAt: null })];
    expect(admitted(rows)).toEqual(['ai-n1']);
    expect(buildSnapshot(rows, NOW).candidates.get('model-a')?.map((c) => c.nodeId)).toEqual(['ai-n1']);
  });
});

describe('candidate status', () => {
  const statusOf = (overrides: Parameters<typeof model>[1]): string | undefined =>
    buildSnapshot([node('ai-n1', [model('model-a', overrides)])], NOW).candidates.get('model-a')?.[0]?.status;

  it('is loaded for a loaded model', () => expect(statusOf({ status: 'loaded' })).toBe('loaded'));
  it('is loadable for a present, verified, loadable, unloaded model', () => expect(statusOf({ status: 'unloaded' })).toBe('loadable'));
  it.each([
    ['not present', { status: 'unloaded', present: false }],
    ['not verified', { status: 'unloaded', verified: false }],
    ['not loadable', { status: 'unloaded', loadable: false }],
    ['loading', { status: 'loading' }],
    ['failed', { status: 'failed' }],
  ] as const)('is no candidate when the model is %s', (_name, overrides) => expect(statusOf(overrides)).toBeUndefined());

  it('carries the node fields, the slots (at least 1) and the context', () => {
    const [c] = buildSnapshot(
      [node('ai-n1', [model('model-a', { slots: 0, ctx: 4096 })], { gatewayPriority: 7, servePattern: 'A', tailnetMachine: 'machine-one', publicHost: 'ai-n1.example.test' })],
      NOW,
    ).candidates.get('model-a')!;
    expect(c).toMatchObject({ nodeId: 'ai-n1', modelId: 'model-a', priority: 7, servePattern: 'A', slots: 1, ctx: 4096, tailnetMachine: 'machine-one', publicHost: 'ai-n1.example.test' });
  });

  it('records a model as present on a node that is not routable (the 503 versus 404 question)', () => {
    const snapshot = buildSnapshot([node('ai-n1', [model('model-a', { status: 'unloaded' })], { mode: 'offline' })], NOW);
    expect(snapshot.present.has('model-a')).toBe(true);
    expect(snapshot.candidates.has('model-a')).toBe(false);
  });
});
