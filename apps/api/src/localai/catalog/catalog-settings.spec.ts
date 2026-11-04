import { githubRepoFromSettings } from './catalog-settings';

describe('catalog.githubRepo setting', () => {
  it('defaults when nothing is stored', () => {
    expect(githubRepoFromSettings(undefined)).toBe('marinoscar/local-ai');
    expect(githubRepoFromSettings({})).toBe('marinoscar/local-ai');
  });

  it('reads the nested and the dotted forms', () => {
    expect(githubRepoFromSettings({ catalog: { githubRepo: 'org-alpha/lab-catalog' } })).toBe('org-alpha/lab-catalog');
    expect(githubRepoFromSettings({ 'catalog.githubRepo': 'org-alpha/lab-catalog' })).toBe('org-alpha/lab-catalog');
  });

  it.each(['evil.example/x/y', 'a/b/c', 'a', '../x/y', 'a/..', 'a b/c', 'a/b?x=1', 42, null])('ignores %j', (value) => {
    expect(githubRepoFromSettings({ catalog: { githubRepo: value } })).toBe('marinoscar/local-ai');
  });
});
