// The install block of the wizard's step 2 (docs/specs/cloud-app.md section 4.8): the pinned script URL, the three
// commands and the script's checksum. Pure: the checksum and the settings come in as arguments.
//
// The script is always the one of a tagged agent release (`node-v<version>`), never `main`, and it is never piped into
// `bash`: it is downloaded, compared with the checksum shown here, then run. The credential is not in any command: the
// installer's `--enroll` asks for it on the terminal with hidden input (installer.md section 3.1).

export interface InstallInfo {
  /** The three commands, one per line. Null while no agent release is configured. */
  oneLiner: string | null;
  pinnedScriptUrl: string | null;
  /** 64 lower-case hex, or null when the release's SHA256SUMS could not be read ("unknown, check the release page"). */
  scriptSha256: string | null;
  /** Why `oneLiner` is null, or why the checksum is unknown; plain text. */
  note: string | null;
  /** The release page, so the operator can compare the checksum by hand when it is unknown. */
  releasePageUrl: string | null;
}

export const SCRIPT_NAME = 'install-node.sh';
const VERSION_RE = /^\d+\.\d+\.\d+$/;
const REPO_RE = /^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/;

export const NO_AGENT_RELEASE_NOTE = 'No agent release is set yet. An administrator sets "Latest agent version" in the Local AI settings; until then use the install command from the release page.';
export const UNKNOWN_CHECKSUM_NOTE = 'Checksum unknown, check the release page.';

export const releaseTag = (version: string) => `node-v${version}`;

export interface InstallInput {
  /** `owner/repo` of the GitHub repository that publishes the agent releases. */
  repo: string;
  /** `settings.agent.latestVersion`, as stored. */
  version: string | null;
  nodeId: string;
  /** `https://ai.marin.cr`. */
  cloudUrl: string;
  sha256: string | null;
}

export function buildInstall(input: InstallInput): InstallInfo {
  const { repo, version } = input;
  if (version === null || !VERSION_RE.test(version) || !REPO_RE.test(repo) || repo.includes('..')) {
    return { oneLiner: null, pinnedScriptUrl: null, scriptSha256: null, note: NO_AGENT_RELEASE_NOTE, releasePageUrl: null };
  }
  const base = `https://github.com/${repo}/releases/download/${releaseTag(version)}`;
  const url = `${base}/${SCRIPT_NAME}`;
  const oneLiner = [
    `curl -fsSLO ${url}`,
    `sha256sum ${SCRIPT_NAME}          # compare with the checksum shown here`,
    `sudo bash ${SCRIPT_NAME} --node-id ${input.nodeId} --cloud-url ${input.cloudUrl} --enroll`,
  ].join('\n');
  return {
    oneLiner,
    pinnedScriptUrl: url,
    scriptSha256: input.sha256,
    note: input.sha256 === null ? UNKNOWN_CHECKSUM_NOTE : null,
    releasePageUrl: `https://github.com/${repo}/releases/tag/${releaseTag(version)}`,
  };
}

/** The checksum of `file` in the text of a `SHA256SUMS` file (`<64 hex>  <name>` per line, `*` before the name allowed). */
export function sha256FromSums(text: string, file: string = SCRIPT_NAME): string | null {
  for (const line of text.split(/\r?\n/)) {
    const match = /^([0-9a-fA-F]{64})[ \t]+\*?(?:\.\/)?(.+?)\s*$/.exec(line);
    if (match && match[2] === file) return match[1]!.toLowerCase();
  }
  return null;
}
