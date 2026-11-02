# Release signing

> **Status:** workflows and tooling are in place. **The signing key does not exist yet**: creating it is an owner action ([§3](#3-owner-action-create-the-key-pair)). Until it is done, the release workflows fail on purpose with a clear message, and no release can be published.
> Related: [security-model.md §4](security-model.md#4-cloud--node), [agent.md §4.10 self-update](../specs/agent.md#410-self-update-internalupdate), [rebuild-kit.md](../specs/rebuild-kit.md), [ADR-0001](../decisions/0001-agent-in-go.md).

Nodes install software from GitHub releases. Anyone who can publish a release could otherwise run code as root on every node. So every release carries a **minisign signature** over its `SHA256SUMS`, and every node checks it against a **public key embedded in the agent**. This page is the trust root: who holds the key, how releases are made, and how the key is replaced.

## 1. What is signed

| Release | Tag | Assets | Checked by |
|---|---|---|---|
| Agent, installer, agent source | `node-v<semver>` | `aiagent-linux-amd64`, `aiagent-linux-arm64`, `SHA256SUMS`, `SHA256SUMS.minisig` | The agent on `sudo aictl update` and the `agent.update` cloud job ([agent.md §4.10](../specs/agent.md#410-self-update-internalupdate)), the installer, `aictl verify-release`, the rebuild kit |
| Catalog and docs | `catalog-<CATALOG_VERSION>` | `catalog.tar.gz`, `SHA256SUMS`, `SHA256SUMS.minisig` | The agent when it refreshes the catalog, the rebuild kit |
| Desktop app | `desktop-v<semver>` | Built by the desktop epic (E8) with the same scripts | The desktop app and the installer |

Tag names and the moving tag `catalog-stable`: [overview.md §3.8](overview.md#3-canonical-names-single-source-of-truth).

`SHA256SUMS` has one line per asset: `<sha256>␣␣<name>`. The signature covers that file only; each asset is then checked against its line. A failed check is `LAI-AGT-010` (critical): the download is discarded and nothing is installed.

## 2. How a release is made

1. A change merges to `main` with CI green.
2. The owner pushes a tag on a commit that is on `main`: `git tag node-v0.1.0 && git push origin node-v0.1.0` (or `catalog-<CATALOG_VERSION>`).
3. The workflow ([release-node.yml](../../.github/workflows/release-node.yml) or [release-catalog.yml](../../.github/workflows/release-catalog.yml)) runs in the protected `release` environment and:
   - refuses a tag that is not well formed or not on `main`;
   - for the agent: runs the same checks and static builds as CI ([`scripts/agent-ci.sh`](../../scripts/agent-ci.sh)), with the version, commit and a fixed build time from the tag;
   - for the catalog: runs `aictl catalog lint catalog/` with the freshly built agent (an unlinted catalog is never published), then builds a reproducible `catalog.tar.gz` of `catalog/` and `docs/`;
   - writes `SHA256SUMS`, signs it ([`scripts/release-sign.sh`](../../scripts/release-sign.sh)), and **verifies the signature with the public key committed in the repository** (so a wrong or stale secret fails the run);
   - creates a **draft** release.
4. **The owner checks the draft, then publishes it.** Nothing is public, and no node sees it, before this step. Check by hand:
   ```
   minisign -Vm SHA256SUMS -p minisign.pub && sha256sum -c SHA256SUMS
   ```
   (`minisign.pub` is [`agent/internal/update/minisign.pub`](#4-the-public-key).)

The workflows do not run until the agent module exists (AGENT-01) and, for the catalog, until `catalog/` exists. They fail with a message instead of publishing something empty.

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
   - turn on **Required reviewers** (you), and under **Deployment branches and tags** allow only the tag patterns `node-v*` and `catalog-*`.
4. **Commit only the public key.** Copy `minisign.pub` to `agent/internal/update/minisign.pub` and paste its second line (it starts with `RW`) into [§4](#4-the-public-key). Commit both in a normal PR. The redaction and gitleaks checks should pass (a public key is not a secret).
5. **Delete the unencrypted copies** you do not need (the working folder), keeping only the backups from step 2.
6. **Prove it works.** Push a throwaway tag such as `node-v0.0.1-test` on a fork, or after AGENT-01 on this repository. When the draft release appears, download its files and run the check from [§2](#2-how-a-release-is-made) step 4. Then delete the draft and the tag.
   - **If it doesn't work:** `signing failed` means a wrong password or a key pasted with a line missing; `SIGNATURE INVALID` means the committed `minisign.pub` is not the pair of the secret key.

## 4. The public key

| | |
|---|---|
| File | `agent/internal/update/minisign.pub` (not committed yet: owner action in §3) |
| Embedded in | the agent (`internal/update/keys.go`, a **list** of keys, [agent.md §4.10](../specs/agent.md#410-self-update-internalupdate)) and the installer |
| Key id and public key | _pending owner action_ |

## 5. Rotation and compromise

Nodes trust only the keys **embedded in the agent they already run**. A node never learns a new key from the internet, so rotation has to be a two-step walk through agent releases. An offline ark laptop updates only when someone updates it, so be patient with it.

**Planned rotation** (new password policy, new custodian, or a key older than you like):
1. Create the new pair as in [§3](#3-owner-action-create-the-key-pair) (as `MINISIGN_SECRET_KEY_NEXT` and `MINISIGN_PASSWORD_NEXT` while both exist).
2. Release an agent that embeds **both** public keys. It is still signed with the **old** key. This is the bridge release.
3. Wait until every node you care about runs the bridge release or newer (`aictl status`, or the Lab page in the cloud). An ark laptop that missed it must be updated by hand with `sudo aictl update` while online, or rebuilt from the AI-ARK drive ([06](../runbooks/06-rebuild-a-node-offline.md)).
4. Switch the secrets to the new key and sign the following releases with it. Update `agent/internal/update/minisign.pub` to the new key in the same PR.
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
