import { checkDraft, describeAliases, describeNodes, describeOnDemand, describePreferred, describeState, draftOf, splitList } from './modelsFormat';
import { modelsFixture } from './modelsFixture';

describe('the model table texts', () => {
  const [coder, big, , whisper] = modelsFixture();

  it('reads like the wireframe', () => {
    expect(describeState('loaded').label).toBe('loaded');
    expect(describeNodes(coder!)).toBe('ai-n1');
    expect(describeNodes(whisper!)).toBe('—');
    expect(describeAliases(coder!.policy.aliases)).toBe('lab-coder');
    expect(describeAliases([])).toBe('—');
    expect(describeOnDemand(big!.policy)).toBe('on · wait 120 s');
    expect(describeOnDemand(coder!.policy)).toBe('off');
    expect(describePreferred(['ai-n2', 'ai-n1'])).toBe('ai-n2 › ai-n1');
    expect(describePreferred([])).toBe('—');
  });

  it('splits a typed list by commas, spaces and lines, without repeats', () => {
    expect(splitList('a-b, c-d\n a-b  e-f')).toEqual(['a-b', 'c-d', 'e-f']);
    expect(splitList('  ')).toEqual([]);
  });
});

describe('checkDraft (the checks the API repeats)', () => {
  const ids = ['qwen3-coder-next-q4km', 'whisper-small'];
  const base = draftOf(modelsFixture()[0]!);

  it('turns a good draft into the PUT body', () => {
    const { input, errors } = checkDraft({ ...base, aliases: 'lab-coder, short-one', preferredNodes: 'ai-n2 ai-n1', loadWaitS: '45', firstByteTimeoutS: '90', onDemandLoad: true }, ids);
    expect(errors).toEqual({});
    expect(input).toEqual({ enabled: true, aliases: ['lab-coder', 'short-one'], onDemandLoad: true, loadWaitS: 45, firstByteTimeoutS: 90, preferredNodes: ['ai-n2', 'ai-n1'] });
  });

  it('an empty first-byte timeout is null', () => {
    expect(checkDraft({ ...base, firstByteTimeoutS: '' }, ids).input?.firstByteTimeoutS).toBeNull();
  });

  const bad: Array<[string, Partial<typeof base>, string]> = [
    ['an alias that is a catalog id', { aliases: 'whisper-small' }, 'aliases'],
    ['an alias with capitals', { aliases: 'Lab Coder' }, 'aliases'],
    ['an alias that is too short', { aliases: 'ab' }, 'aliases'],
    ['too many aliases', { aliases: Array.from({ length: 17 }, (_, i) => `alias-${i}`).join(',') }, 'aliases'],
    ['a node that is not ai-nN', { preferredNodes: 'laptop' }, 'preferredNodes'],
    ['a wait below 10', { loadWaitS: '9' }, 'loadWaitS'],
    ['a wait above 600', { loadWaitS: '601' }, 'loadWaitS'],
    ['a fractional wait', { loadWaitS: '12.5' }, 'loadWaitS'],
    ['an empty wait', { loadWaitS: '' }, 'loadWaitS'],
    ['a first-byte timeout of 0', { firstByteTimeoutS: '0' }, 'firstByteTimeoutS'],
  ];
  it.each(bad)('refuses %s', (_name, patch, field) => {
    const result = checkDraft({ ...base, ...patch }, ids);
    expect(result.input).toBeNull();
    expect(Object.keys(result.errors)).toContain(field);
  });
});
