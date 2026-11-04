import { candidate } from './routing.fixtures';
import { compareCandidates, isSaturated, selectLoaded, unavailableOutcome, type RankInput } from './select';
import type { RoutingPolicy, SelectedCandidate } from './routing.types';

const open = { suspect: false, keyInvalid: false };
const input = (c: SelectedCandidate, flags: Partial<typeof open> = {}): RankInput => ({ candidate: c, flags: { ...open, ...flags } });
const ids = (result: ReturnType<typeof selectLoaded>): string[] => (result.kind === 'candidates' ? result.candidates.map((c) => c.nodeId) : [result.kind]);
const NO_TRIED = new Set<string>();
const policy = (...preferredNodes: string[]): RoutingPolicy => ({ preferredNodes });

describe('selectLoaded: the order (priority, preference, load, first byte, node id)', () => {
  type Case = [string, SelectedCandidate[], RoutingPolicy, string[]];
  const cases: Case[] = [
    ['lower priority number first', [candidate('ai-n1', { priority: 200 }), candidate('ai-n2', { priority: 50 })], policy(), ['ai-n2', 'ai-n1']],
    ['priority beats preference', [candidate('ai-n1', { priority: 200 }), candidate('ai-n2', { priority: 50 })], policy('ai-n1'), ['ai-n2', 'ai-n1']],
    ['preference breaks a priority tie', [candidate('ai-n1'), candidate('ai-n2'), candidate('ai-n3')], policy('ai-n3', 'ai-n2'), ['ai-n3', 'ai-n2', 'ai-n1']],
    ['a node absent from preferredNodes sorts last', [candidate('ai-n1'), candidate('ai-n2')], policy('ai-n2'), ['ai-n2', 'ai-n1']],
    ['preference beats a lighter load', [candidate('ai-n1', { inflight: 0 }), candidate('ai-n2', { inflight: 3 })], policy('ai-n2'), ['ai-n2', 'ai-n1']],
    ['inflight / slots, not inflight alone', [candidate('ai-n1', { inflight: 2, slots: 2 }), candidate('ai-n2', { inflight: 3, slots: 8 })], policy(), ['ai-n2', 'ai-n1']],
    ['equal load: the faster first byte first', [candidate('ai-n1', { ewmaTtfbMs: 900 }), candidate('ai-n2', { ewmaTtfbMs: 300 })], policy(), ['ai-n2', 'ai-n1']],
    ['equal load: an unmeasured node is tried before a measured one', [candidate('ai-n1', { ewmaTtfbMs: 300 }), candidate('ai-n2', { ewmaTtfbMs: null })], policy(), ['ai-n2', 'ai-n1']],
    ['everything equal: node id', [candidate('ai-n2'), candidate('ai-n1'), candidate('ai-n3')], policy(), ['ai-n1', 'ai-n2', 'ai-n3']],
    ['load beats first byte', [candidate('ai-n1', { inflight: 1, ewmaTtfbMs: 10 }), candidate('ai-n2', { inflight: 0, ewmaTtfbMs: 5000 })], policy(), ['ai-n2', 'ai-n1']],
  ];
  it.each(cases)('%s', (_name, candidates, pol, expected) => {
    expect(ids(selectLoaded(candidates.map((c) => input(c)), pol, NO_TRIED, 4))).toEqual(expected);
  });

  it('is a total order: the comparator is antisymmetric on every pair of the cases', () => {
    const all = [candidate('ai-n1'), candidate('ai-n2', { priority: 1 }), candidate('ai-n3', { inflight: 1 }), candidate('ai-n4', { ewmaTtfbMs: 5 })];
    for (const a of all) for (const b of all) expect(Math.sign(compareCandidates(a, b, policy())) + Math.sign(compareCandidates(b, a, policy()))).toBe(0);
  });
});

describe('selectLoaded: who is eligible', () => {
  it('leaves out a suspect node', () => {
    expect(ids(selectLoaded([input(candidate('ai-n1'), { suspect: true }), input(candidate('ai-n2'))], policy(), NO_TRIED, 4))).toEqual(['ai-n2']);
  });
  it('leaves out a keyInvalid node', () => {
    expect(ids(selectLoaded([input(candidate('ai-n1'), { keyInvalid: true }), input(candidate('ai-n2'))], policy(), NO_TRIED, 4))).toEqual(['ai-n2']);
  });
  it('leaves out a node already tried', () => {
    expect(ids(selectLoaded([input(candidate('ai-n1')), input(candidate('ai-n2'))], policy(), new Set(['ai-n1']), 4))).toEqual(['ai-n2']);
  });
  it('ignores a candidate that is only loadable', () => {
    expect(ids(selectLoaded([input(candidate('ai-n1', { status: 'loadable' }))], policy(), NO_TRIED, 4))).toEqual(['none_loaded']);
  });
  it('says none_loaded when every loaded candidate is suspect, invalid or tried', () => {
    const inputs = [input(candidate('ai-n1'), { suspect: true }), input(candidate('ai-n2'), { keyInvalid: true }), input(candidate('ai-n3'))];
    expect(ids(selectLoaded(inputs, policy(), new Set(['ai-n3']), 4))).toEqual(['none_loaded']);
  });
  it('says none_loaded for no candidates at all', () => {
    expect(ids(selectLoaded([], policy(), NO_TRIED, 4))).toEqual(['none_loaded']);
  });
});

describe('selectLoaded: saturation (inflight >= queueFactor x slots)', () => {
  it('is saturated at exactly queueFactor x slots, not below', () => {
    expect(isSaturated({ inflight: 7, slots: 2 }, 4)).toBe(false);
    expect(isSaturated({ inflight: 8, slots: 2 }, 4)).toBe(true);
  });
  it('treats a slot count below 1 as 1', () => {
    expect(isSaturated({ inflight: 4, slots: 0 }, 4)).toBe(true);
  });
  it('skips a saturated best candidate and tries the next', () => {
    const full = candidate('ai-n1', { priority: 1, inflight: 8, slots: 2 });
    expect(ids(selectLoaded([input(full), input(candidate('ai-n2'))], policy(), NO_TRIED, 4))).toEqual(['ai-n2']);
  });
  it('says saturated (LAI-GW-023) when every eligible candidate is full', () => {
    const inputs = [input(candidate('ai-n1', { inflight: 8, slots: 2 })), input(candidate('ai-n2', { inflight: 4, slots: 1 }))];
    expect(selectLoaded(inputs, policy(), NO_TRIED, 4)).toEqual({ kind: 'saturated' });
  });
  it('honours a different queueFactor', () => {
    const c = candidate('ai-n1', { inflight: 2, slots: 2 });
    expect(selectLoaded([input(c)], policy(), NO_TRIED, 1)).toEqual({ kind: 'saturated' });
    expect(selectLoaded([input(c)], policy(), NO_TRIED, 2).kind).toBe('candidates');
  });
  it('is not saturated when the full node is suspect and nothing else is loaded: that is none_loaded', () => {
    expect(selectLoaded([input(candidate('ai-n1', { inflight: 99 }), { suspect: true })], policy(), NO_TRIED, 4)).toEqual({ kind: 'none_loaded' });
  });
});

describe('unavailableOutcome (step 4)', () => {
  const cases: Array<[string, boolean, boolean, number, string]> = [
    ['in the approved catalog and present on some node: 503 no capacity', true, true, 503, 'LAI-GW-021'],
    ['in the approved catalog but present nowhere: 404 unknown', true, false, 404, 'LAI-GW-020'],
    ['present on a node but not in the approved catalog: 404 unknown', false, true, 404, 'LAI-GW-020'],
    ['neither: 404 unknown', false, false, 404, 'LAI-GW-020'],
  ];
  it.each(cases)('%s', (_name, inApprovedCatalog, presentOnSomeNode, status, code) => {
    expect(unavailableOutcome({ inApprovedCatalog, presentOnSomeNode })).toEqual({ status, code });
  });
});
