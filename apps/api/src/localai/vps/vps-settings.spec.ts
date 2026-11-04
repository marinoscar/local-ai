import { vpsSettingsFrom } from './vps-settings';

describe('vpsSettingsFrom', () => {
  it('defaults to production: empty suffix, ai.marin.cr', () => {
    expect(vpsSettingsFrom(undefined)).toEqual({ stagingSuffix: '', stagingSuffixValid: true, vpsPublicHostname: 'ai.marin.cr' });
    expect(vpsSettingsFrom({})).toMatchObject({ stagingSuffix: '', stagingSuffixValid: true });
  });

  it.each([
    [{ vps: { stagingSuffix: '-staging' } }, '-staging'],
    [{ 'vps.stagingSuffix': '-staging' }, '-staging'],
    [{ vps: { stagingSuffix: '' } }, ''],
  ])('reads %j', (stored, expected) => expect(vpsSettingsFrom(stored)).toMatchObject({ stagingSuffix: expected, stagingSuffixValid: true }));

  it.each([['staging'], ['-Staging'], ['-a b'], ['-x'.repeat(10)], ['; rm'], [3], [{}]])('reports %j as invalid, not as the default', (value) => {
    expect(vpsSettingsFrom({ vps: { stagingSuffix: value } })).toEqual({ stagingSuffix: '', stagingSuffixValid: false, vpsPublicHostname: 'ai.marin.cr' });
  });

  it('keeps the VPS public hostname from the checklist setting', () => {
    expect(vpsSettingsFrom({ checklist: { vpsPublicHostname: 'cloud.marin.cr' } }).vpsPublicHostname).toBe('cloud.marin.cr');
  });
});
