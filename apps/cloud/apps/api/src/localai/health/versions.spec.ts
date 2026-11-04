import { isAgentBehind, isCatalogBehind, parseSemver } from './versions';

describe('parseSemver', () => {
  it.each<[string | null, [number, number, number] | null]>([
    ['0.4.1', [0, 4, 1]],
    ['v0.4.1', [0, 4, 1]],
    ['10.20.30', [10, 20, 30]],
    ['1.2.3-rc1', [1, 2, 3]],
    ['1.2.3+build5', [1, 2, 3]],
    ['1.2', null],
    ['latest', null],
    ['', null],
    [null, null],
  ])('%s', (value, expected) => {
    expect(parseSemver(value)).toEqual(expected);
  });
});

describe('isAgentBehind', () => {
  it.each<[string | null, string | null, boolean]>([
    ['0.4.0', '0.4.1', true],
    ['0.4.1', '0.4.1', false],
    ['0.4.2', '0.4.1', false],
    ['0.3.9', '0.4.0', true],
    ['0.9.0', '0.10.0', true],
    ['1.0.0', '0.99.99', false],
    ['0.4.0', null, false],
    [null, '0.4.1', false],
    ['dev', '0.4.1', false],
    ['0.4.0', 'next', false],
  ])('agent %s vs latest %s -> %s', (agent, latest, expected) => {
    expect(isAgentBehind(agent, latest)).toBe(expected);
  });
});

describe('isCatalogBehind', () => {
  it.each<[string | null, string | null, boolean]>([
    ['2026.10.28-2', '2026.11.02-1', true],
    ['2026.11.02-1', '2026.11.02-1', false],
    ['2026.11.02-1', null, false],
    [null, '2026.11.02-1', false],
    [null, null, false],
  ])('node %s vs approved %s -> %s', (node, approved, expected) => {
    expect(isCatalogBehind(node, approved)).toBe(expected);
  });
});
