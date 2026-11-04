import { auditQueryString, dayEndIso, dayStartIso, formatWhen, rangeIsBackwards, targetLabel } from './format';
import { EMPTY_FILTERS } from './types';

describe('formatWhen', () => {
  it('prints a 24 hour clock in the named zone', () => {
    expect(formatWhen('2026-11-03T15:04:09.000Z', 'UTC')).toBe('03 Nov 2026, 15:04:09');
    expect(formatWhen('2026-11-03T15:04:09.000Z', 'Asia/Tokyo')).toBe('04 Nov 2026, 00:04:09');
  });
  it('returns an unreadable value as it came', () => {
    expect(formatWhen('not a date')).toBe('not a date');
  });
});

describe('targetLabel', () => {
  it.each([
    [{ targetType: 'localai_node', targetId: 'ai-n3' }, 'node ai-n3'],
    [{ targetType: 'localai_command', targetId: '6b3c1c9e-7a52-4f4e-9d5e-0a1b2c3d4e5f' }, 'command 6b3c1c9e'],
    [{ targetType: 'localai_catalog', targetId: '2026.11.02-1' }, 'catalog 2026.11.02-1'],
    [{ targetType: 'localai_alert', targetId: '4a1e5c2d-9b7f-4d3a-8c6e-1f2a3b4c5d6e' }, 'alert 4a1e5c2d'],
    [{ targetType: 'localai_gateway_key', targetId: 'gwk-1' }, 'gateway key gwk-1'],
    [{ targetType: 'localai_vps', targetId: 'route53' }, 'Route53 key'],
    [{ targetType: 'localai_future_thing', targetId: 'f-1' }, 'future thing f-1'],
  ])('%j -> %s', (row, label) => {
    expect(targetLabel(row)).toBe(label);
  });
});

describe('the date range', () => {
  it('turns a calendar day into the start and end of that day in the viewer zone', () => {
    const start = new Date(dayStartIso('2026-11-02')!);
    const end = new Date(dayEndIso('2026-11-02')!);
    expect([start.getFullYear(), start.getMonth(), start.getDate(), start.getHours(), start.getMinutes()]).toEqual([2026, 10, 2, 0, 0]);
    expect([end.getFullYear(), end.getMonth(), end.getDate(), end.getHours(), end.getMinutes(), end.getSeconds()]).toEqual([2026, 10, 2, 23, 59, 59]);
  });
  it('ignores something that is not a day', () => {
    expect(dayStartIso('')).toBeUndefined();
    expect(dayEndIso('11/02/2026')).toBeUndefined();
  });
  it('knows a backwards range', () => {
    expect(rangeIsBackwards({ from: '2026-11-03', to: '2026-11-01' })).toBe(true);
    expect(rangeIsBackwards({ from: '2026-11-01', to: '2026-11-01' })).toBe(false);
    expect(rangeIsBackwards({ from: '', to: '2026-11-01' })).toBe(false);
  });
});

describe('auditQueryString', () => {
  it('is empty with no filters', () => {
    expect(auditQueryString(EMPTY_FILTERS)).toBe('');
  });
  it('carries every set filter and the page from 2', () => {
    const params = new URLSearchParams(auditQueryString({ nodeId: 'ai-n2', action: 'localai.command.', from: '2026-11-01', to: '2026-11-02', page: 3 }));
    expect(params.get('nodeId')).toBe('ai-n2');
    expect(params.get('action')).toBe('localai.command.');
    expect(params.get('from')).toBe(dayStartIso('2026-11-01'));
    expect(params.get('to')).toBe(dayEndIso('2026-11-02'));
    expect(params.get('page')).toBe('3');
  });
  it('leaves page 1 out', () => {
    expect(new URLSearchParams(auditQueryString({ ...EMPTY_FILTERS, nodeId: 'ai-n1' })).has('page')).toBe(false);
  });
});
