import { DEFAULT_VPS_PUBLIC_HOSTNAME, wizardSettingsFrom } from './wizard-settings';

describe('wizardSettingsFrom', () => {
  it('uses the documented defaults when nothing is stored', () => {
    for (const stored of [undefined, null, 'x', [], {}]) {
      expect(wizardSettingsFrom(stored)).toEqual({ agentLatestVersion: null, vpsPublicHostname: DEFAULT_VPS_PUBLIC_HOSTNAME, githubRepo: 'marinoscar/local-ai' });
    }
  });
  it('reads the nested and the dotted forms', () => {
    expect(wizardSettingsFrom({ agent: { latestVersion: '0.4.1' }, checklist: { vpsPublicHostname: 'Cloud.Example.Test' }, catalog: { githubRepo: 'org-alpha/lab' } })).toEqual({
      agentLatestVersion: '0.4.1',
      vpsPublicHostname: 'cloud.example.test',
      githubRepo: 'org-alpha/lab',
    });
    expect(wizardSettingsFrom({ 'agent.latestVersion': '1.2.3', 'checklist.vpsPublicHostname': 'cloud.example.test' })).toMatchObject({ agentLatestVersion: '1.2.3', vpsPublicHostname: 'cloud.example.test' });
  });
  it('ignores a value that is not safe to put in a URL, a command or a DNS query', () => {
    expect(wizardSettingsFrom({ agent: { latestVersion: 'latest' }, checklist: { vpsPublicHostname: 'localhost' } })).toMatchObject({ agentLatestVersion: null, vpsPublicHostname: DEFAULT_VPS_PUBLIC_HOSTNAME });
    expect(wizardSettingsFrom({ agent: { latestVersion: '1.2.3/../x' } }).agentLatestVersion).toBeNull();
  });
});
