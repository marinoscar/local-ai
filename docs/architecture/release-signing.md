# Release signing

> **Status:** workflows and tooling are in place. **The signing key does not exist yet**: creating it is an owner action ([§3](#3-owner-action-create-the-key-pair)). Until it is done, the release workflows fail on purpose with a clear message, and no release can be published.
> Related: [security-model.md §4](security-model.md#4-cloud--node), [agent.md §4.10 self-update](../specs/agent.md#410-self-update-internalupdate), [rebuild-kit.md](../specs/rebuild-kit.md), [ADR-0001](../decisions/0001-agent-in-go.md).

Nodes install software from GitHub releases. Anyone who can publish a release could otherwise run code as root on every node. So every release carries a **minisign signature** over its `SHA256SUMS`, and every node checks it against a **public key embedded in the agent**. This page is the trust root: who holds the key, how releases are made, and how the key is replaced.

## 1. What is signed

| Release | Tag | Assets | Checked by |
|---|---|---|---|
| Agent, installer, agent source | `node-v<semver>` | `aiagent-linux-amd64`, `aiagent-linux-arm64`, `install-node.sh`, `local-ai-node-v<semver>-src.tar.gz`, `SHA256SUMS` (lists the other four), `SHA256SUMS.minisig` | The agent on `sudo aictl update` and the `agent.update` cloud job ([agent.md §4.10](../specs/agent.md#410-self-update-internalupdate)), the installer, `aictl verify-release`, the rebuild kit |
| Catalog and docs | `catalog-<CATALOG_VERSION>` | `catalog.tar.gz`, `SHA256SUMS`, `SHA256SUMS.minisig` | The agent when it refreshes the catalog, the rebuild kit |
| Desktop app | `desktop-v<semver>` | `ai-desktop_<v>_amd64.deb`, `ai-desktop-<v>-x86_64.AppImage`, `latest-linux.yml`, `SHA256SUMS` (lists those three), `SHA256SUMS.minisig` | `aictl verify-release`, the rebuild kit and the person installing; the opt-in updater reads `latest-linux.yml` and checks the SHA-512 of the file it downloads ([desktop.md §4.10](../specs/desktop.md#410-updates-off-by-default)) |

Tag names and the moving tag `catalog-stable`: [overview.md §3.8](overview.md#3-canonical-names-single-source-of-truth).

`SHA256SUMS` has one line per asset: `<sha256>␣␣<name>`. The signature covers that file only; each asset is then checked against its line. A failed check is `LAI-AGT-010` (critical): the download is discarded and nothing is installed.

## 2. How a release is made

1. A change merges to `main` with CI green.
2. The owner pushes a tag on a commit that is on `main`: `git tag node-v0.1.0 && git push origin node-v0.1.0` (or `catalog-<CATALOG_VERSION>`).
3. The workflow ([release-node.yml](../../.github/workflows/release-node.yml) or [release-catalog.yml](../../.github/workflows/release-catalog.yml)) runs in the protected `release` environment and:
   - refuses a tag that is not well formed or not on `main`;
   - for the agent: runs the same checks and static builds as CI ([`scripts/agent-ci.sh`](../../scripts/agent-ci.sh)), with the version, commit and a fixed build time from the tag; builds the local UI (when `agent/ui/` exists) and the reproducible source tarball (built twice, the two must be identical, [rebuild-kit.md](../specs/rebuild-kit.md) §4.1.1); proves in a network namespace (`unshare -n`) that the tarball builds offline and reports the release version; and only then, in the protected `release` environment, stamps the version and the public key into `install-node.sh` ([`scripts/release/assemble-node-release.sh`](../../scripts/release/assemble-node-release.sh));
   - for the catalog: runs `aictl catalog lint catalog/` with the freshly built agent (an unlinted catalog is never published), then builds a reproducible `catalog.tar.gz` of `catalog/` and `docs/`;
   - writes `SHA256SUMS`, signs it ([`scripts/release-sign.sh`](../../scripts/release-sign.sh)), and **verifies the signature with the public key committed in the repository** (so a wrong or stale secret fails the run);
   - creates a **draft** release.
4. **The owner checks the draft, then publishes it.** Nothing is public, and no node sees it, before this step. Check by hand:
   ```
   minisign -Vm SHA256SUMS -p minisign.pub && sha256sum -c SHA256SUMS
   ```
   (`minisign.pub` is [`agent/internal/kit/keys/minisign.pub`](#4-the-public-key).)

**The desktop app** ([desktop-release.yml](../../.github/workflows/desktop-release.yml), tag `desktop-v<semver>`, [desktop.md §4.11](../specs/desktop.md#411-packaging-and-release)) follows the same trust model with its own build. The tag must equal `desktop-v` plus the version in `apps/desktop/package.json` and sit on `main` (a mismatch fails the first job; fix it with a PR and a **new** tag, a pushed tag is never moved). The `build` job (no secrets) typechecks, tests, packages, verifies the packaged app, installs the deb as a node would and runs the packaged end-to-end suite; only then does the `release` job (environment `release`) assemble the assets ([`scripts/release/assemble-desktop-release.sh`](../../scripts/release/assemble-desktop-release.sh), which also checks that `latest-linux.yml` names this version and the real sha512 and size of the deb and the AppImage), write and sign `SHA256SUMS` with the same key and password secrets as the agent (`MINISIGN_SECRET_KEY`, `MINISIGN_PASSWORD`), verify the signature with the committed public key, create a **draft**, check that it holds the five assets, **publish** it (a pre-release when the tag has a `-`, such as `desktop-v0.1.0-rc.1`; never marked "latest"), download what GitHub now serves and verify it again with the committed key, moving the release back to a draft if that fails. The release job waits for the `release` environment's required reviewers, which is the owner's chance to stop it. Afterwards a PR from the [`kit-desktop-bump`](../../.github/PULL_REQUEST_TEMPLATE/kit-desktop-bump.md) template pins the deb in the rebuild kit.

The workflows do not run until the agent module exists (AGENT-01) and, for the catalog, until `catalog/` exists. They fail with a message instead of publishing something empty. The node release also fails until `installers/install-node.sh` exists (INST-01): a node release always carries its installer.

**Dry run.** Actions → release-node (or desktop-release) → *Run workflow* on any branch runs the same build, source tarball and offline proof, then assembles and signs the assets with a **throwaway key created inside the job** and uploads them as a workflow artifact (`node-release-dry-run`, 7 days). No release is created and the real key is never used; it also works before the owner has created the key pair. Without an `installers/install-node.sh` the dry run ships five assets and says so. The desktop dry run builds, verifies and runs the packaged end-to-end suite, assembles the five desktop assets, signs them with a throwaway key and uploads them as `desktop-release-dry-run`; it skips the `release` job.

## 3. Owner action: create the key pair

Do this once, on a computer you trust, **with no internet connection** if you can. It takes about 15 minutes. You need the `minisign` program (`sudo apt install minisign` on Ubuntu).

1. **Generate the key pair.** In an empty folder:
   ```
   minisign -G -p minisign.pub -s minisign.key
   ```
   Choose a long password when asked, and remember it.
   - **What you should see:** two new files, `minisign.pub` (public, safe to share) and `minisign.key` (secret, encrypted with your password).
2. **Make the backups of the secret key and the password.** Both are needed to release; losing either means rotating to a new key ([§5](#5-rotation-and-compromise)).
   - Print `minisign.key` on paper, or copy it to two offline USB sticks, and keep the password separately (password manager plus a sealed paper copy).
   - **Never** put `minisign.key` or the password on the AI-ARK drive, in the repository, in a chat or in an email. The ark drive travels; the key must not.
3. **Store them as GitHub secrets.** In the repository: **Settings → Environments → New environment → `release`**. Under the environment:
   - add secret `MINISIGN_SECRET_KEY` with the **entire contents** of `minisign.key` (both lines);
   - add secret `MINISIGN_PASSWORD` with the password;
   - turn on **Required reviewers** (you), and under **Deployment branches and tags** allow only the tag patterns `node-v*`, `catalog-*` and `desktop-v*`.
4. **Commit only the public key.** Copy `minisign.pub` to `agent/internal/kit/keys/minisign.pub` and paste its second line (it starts with `RW`) into [§4](#4-the-public-key). Commit both in a normal PR. The redaction and gitleaks checks should pass (a public key is not a secret).
5. **Delete the unencrypted copies** you do not need (the working folder), keeping only the backups from step 2.
6. **Prove it works.** Push a throwaway tag such as `node-v0.0.1-test` on a fork, or after AGENT-01 on this repository. When the draft release appears, download its files and run the check from [§2](#2-how-a-release-is-made) step 4. Then delete the draft and the tag.
   - **If it doesn't work:** `signing failed` means a wrong password or a key pasted with a line missing; `SIGNATURE INVALID` means the committed `minisign.pub` is not the pair of the secret key.

## 4. The public key

| | |
|---|---|
| File | `agent/internal/kit/keys/minisign.pub` (committed) |
| Embedded in | the agent (`internal/update/keys.go` embeds every `minisign*.pub` file of that folder, so a rotation's bridge key is a second file next to `minisign.pub`; with none committed the list is empty and every update fails closed with `LAI-AGT-010`, [agent.md §4.10](../specs/agent.md#410-self-update-internalupdate)) and the installer (stamped at release time, [installer.md §3.6](../specs/installer.md#36-release-stamping)); this file is the single source of both, and the workflow verifies the signature with it |
| Key id and public key | `947F84B6E9587262`, `RWRicljptoR/lC6hylzH+n2wr9Kh8no9x1gDBKGle8MJIX53xbzT8w3u` |

## 5. Rotation and compromise

Nodes trust only the keys **embedded in the agent they already run**. A node never learns a new key from the internet, so rotation has to be a two-step walk through agent releases. An offline ark laptop updates only when someone updates it, so be patient with it.

**Planned rotation** (new password policy, new custodian, or a key older than you like):
1. Create the new pair as in [§3](#3-owner-action-create-the-key-pair) (as `MINISIGN_SECRET_KEY_NEXT` and `MINISIGN_PASSWORD_NEXT` while both exist).
2. Release an agent that embeds **both** public keys. It is still signed with the **old** key. This is the bridge release.
3. Wait until every node you care about runs the bridge release or newer (`aictl status`, or the Lab page in the cloud). An ark laptop that missed it must be updated by hand with `sudo aictl update` while online, or rebuilt from the AI-ARK drive ([06](../runbooks/06-rebuild-a-node-offline.md)).
4. Switch the secrets to the new key and sign the following releases with it. Update `agent/internal/kit/keys/minisign.pub` to the new key in the same PR.
5. Later, release an agent that embeds only the new key.

A node that never ran the bridge release **cannot** verify new-key releases: it refuses them with `LAI-AGT-010`. That is the safe failure. Fix it by installing the current agent from the rebuild kit.

**If the secret key or its password may have leaked:**
1. Remove `agent.update` from `cloud.allow` on the nodes that matter, so a forged update cannot be pushed from the cloud ([security-model.md §4](security-model.md#4-cloud--node)).
2. Delete the GitHub secrets and any draft or recent release you did not make.
3. Create a new pair and follow the planned rotation, but ship the bridge release by hand (installer or kit), because the old key can no longer be trusted to sign it.
4. Write down what happened and when, and check the audit log on each node for unexpected `agent.update` entries.

## 6. What the tooling guarantees

Tested by `scripts/tests/test-release-sign.sh` with throwaway keys (never the real one):
- `SHA256SUMS` is `<sha256>␣␣<name>`, sorted, and written atomically;
- a changed binary, a changed `SHA256SUMS` or a different key each make verification fail;
- the secret key lives in a mode-0600 temporary file removed on exit; the password goes over stdin, never on a command line; neither is ever printed, even when signing fails.

`scripts/tests/test-release-node.sh` (also throwaway keys, plus a tiny fixture repository) adds, for the node release scripts in `scripts/release/`:
- the source tarball is byte-identical across two builds, a different umask and a fresh clone; it has sorted entries, one fixed mtime, owner 0:0 and no top-level folder; a stale committed `vendor/`, a redaction hit, or a UI project without a built `dist/` stops the build;
- `install-node.sh` is stamped with the version and the key of the committed file, and a template without the three tokens (or with an unknown one) is refused;
- the assembled release has exactly the six assets, `SHA256SUMS` lists the other four, `minisign -V` and `sha256sum -c` pass, and a secret that is not the committed key's pair fails at assembly time;
- the offline build (`unshare -n`, `GOPROXY=off`) succeeds from the tarball, fails with `LAI-KIT-030` when a module is not vendored, and fails when the version stamp does not take effect.
