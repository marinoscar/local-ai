import { buildInstall, NO_AGENT_RELEASE_NOTE, releaseTag, sha256FromSums, UNKNOWN_CHECKSUM_NOTE } from './install-info';

const SHA = 'ab'.repeat(32);
const base = { repo: 'marinoscar/local-ai', version: '0.4.1', nodeId: 'ai-n4', cloudUrl: 'https://ai.marin.cr', sha256: SHA };

describe('buildInstall', () => {
  it('pins the script to the agent release tag, never main', () => {
    const install = buildInstall(base);
    expect(install.pinnedScriptUrl).toBe('https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh');
    expect(install.oneLiner).not.toMatch(/\bmain\b/);
    expect(releaseTag('0.4.1')).toBe('node-v0.4.1');
  });

  it('prints download, checksum and run as three lines; the script is never piped into a shell', () => {
    const lines = buildInstall(base).oneLiner!.split('\n');
    expect(lines).toHaveLength(3);
    expect(lines[0]).toBe('curl -fsSLO https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh');
    expect(lines[1]).toBe(`echo "${SHA}  install-node.sh" | sha256sum -c          # must print: install-node.sh: OK`);
    expect(lines[2]).toBe('sudo bash install-node.sh --node-id ai-n4 --cloud-url https://ai.marin.cr --enroll');
    expect(buildInstall(base).oneLiner).not.toMatch(/\|\s*(sudo\s+)?(ba)?sh\b/);
  });

  it('the check compares the checksum itself and prints OK or FAILED (#437); with no checksum it only prints the sum', () => {
    expect(buildInstall(base).oneLiner).toContain(`echo "${SHA}  install-node.sh" | sha256sum -c`);
    const unknown = buildInstall({ ...base, sha256: null }).oneLiner!.split('\n');
    expect(unknown[1]).toMatch(/^sha256sum install-node\.sh\s+# compare with the checksum on the release page$/);
    expect(unknown[1]).not.toContain('| sha256sum -c');
  });

  it('has no credential in it: --enroll asks for it on the terminal', () => {
    const text = JSON.stringify(buildInstall(base));
    expect(text).not.toMatch(/nod_/);
    expect(text).not.toMatch(/--(token|credential|password)/);
  });

  it('carries the checksum, or says it is unknown and links the release page', () => {
    expect(buildInstall(base)).toMatchObject({ scriptSha256: SHA, note: null, releasePageUrl: 'https://github.com/marinoscar/local-ai/releases/tag/node-v0.4.1' });
    expect(buildInstall({ ...base, sha256: null })).toMatchObject({ scriptSha256: null, note: UNKNOWN_CHECKSUM_NOTE });
  });

  it('shows no commands while no agent release is configured (and still never falls back to main)', () => {
    expect(buildInstall({ ...base, version: null })).toEqual({ oneLiner: null, pinnedScriptUrl: null, scriptSha256: null, note: NO_AGENT_RELEASE_NOTE, releasePageUrl: null });
  });

  it.each(['0.4', 'main', '0.4.1; rm -rf /', '../1.2.3', ' 0.4.1', 'v0.4.1'])('refuses the version %j instead of putting it in a URL', (version) => {
    expect(buildInstall({ ...base, version }).oneLiner).toBeNull();
  });

  it.each(['owner', 'owner/repo/extra', '../x/y', 'a b/c', 'o/..'])('refuses the repository %j', (repo) => {
    expect(buildInstall({ ...base, repo }).oneLiner).toBeNull();
  });
});

describe('sha256FromSums', () => {
  const other = 'cd'.repeat(32);
  it('finds the install script among the release assets', () => {
    expect(sha256FromSums(`${other}  aiagent-linux-amd64\n${SHA.toUpperCase()}  install-node.sh\n`)).toBe(SHA);
  });
  it('accepts the binary-mode marker and a ./ prefix, and CRLF', () => {
    expect(sha256FromSums(`${SHA} *install-node.sh\r\n`)).toBe(SHA);
    expect(sha256FromSums(`${SHA}  ./install-node.sh\n`)).toBe(SHA);
  });
  it('does not match another file whose name merely ends the same', () => {
    expect(sha256FromSums(`${SHA}  old-install-node.sh\n${SHA}  install-node.sh.sig\n`)).toBeNull();
  });
  it('is null for a malformed or empty file', () => {
    expect(sha256FromSums('')).toBeNull();
    expect(sha256FromSums('not a checksum file')).toBeNull();
    expect(sha256FromSums(`${'z'.repeat(64)}  install-node.sh`)).toBeNull();
    expect(sha256FromSums(`${'a'.repeat(63)}  install-node.sh`)).toBeNull();
  });
});
