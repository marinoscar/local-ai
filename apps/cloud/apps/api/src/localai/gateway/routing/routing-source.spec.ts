import { parseRoutingSettings } from './routing-source';

describe('parseRoutingSettings (gateway.queueFactor)', () => {
  const cases: Array<[string, unknown, number]> = [
    ['nothing stored', undefined, 4],
    ['an empty object', {}, 4],
    ['the nested form', { gateway: { queueFactor: 6 } }, 6],
    ['the dotted key', { 'gateway.queueFactor': 2 }, 2],
    ['the nested form wins over the dotted key', { gateway: { queueFactor: 3 }, 'gateway.queueFactor': 9 }, 3],
    ['zero', { gateway: { queueFactor: 0 } }, 4],
    ['a fraction', { gateway: { queueFactor: 1.5 } }, 4],
    ['above 64', { gateway: { queueFactor: 65 } }, 4],
    ['exactly 64', { gateway: { queueFactor: 64 } }, 64],
    ['a string', { gateway: { queueFactor: '8' } }, 4],
    ['not an object', 'x', 4],
  ];
  it.each(cases)('%s', (_name, stored, expected) => {
    expect(parseRoutingSettings(stored)).toEqual({ queueFactor: expected });
  });
});
