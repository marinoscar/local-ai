<!--
Use this template with ?template=kit-desktop-bump.md on the compare page (or paste it into the PR body).
Spec: docs/specs/desktop.md section 4.11 step 5; the item and the kit layout: docs/specs/rebuild-kit.md section 3.
Branch: chore/<issue#>-kit-desktop-<version>. Open it AFTER the desktop release is published.
-->
## Summary
Bump the catalog item `kit-desktop` to the desktop release **desktop-v<version>**, so ark sync places this version of ai-desktop in `<root>/kit/desktop/` and an offline node can reinstall it with `sudo apt install <root>/kit/desktop/ai-desktop_<version>_amd64.deb`.

## Linked issue
Relates to #

## The release being pinned
| | |
|---|---|
| Tag | `desktop-v<version>` |
| Release page | `https://github.com/marinoscar/local-ai/releases/tag/desktop-v<version>` (published, not a draft) |
| Pre-release? | yes / no (a `-rc` tag is a pre-release; pin one only on purpose) |

| Asset | Size (bytes) | sha256 |
|---|---|---|
| `ai-desktop_<version>_amd64.deb` (the file the offline `apt install` uses) | | |
| `ai-desktop-<version>-x86_64.AppImage` | | |

Fill the table from the release notes or from the command below, never by hand from memory. `expected_size_bytes` of the item is the sum of the two sizes, rounded up to the next 1,000,000.

## How the pins were produced
1. Download the release and check it against the agent's key (no pin is taken from a file that does not verify):
   ```
   gh release download desktop-v<version> --repo marinoscar/local-ai --dir /tmp/desktop-v<version>
   scripts/release/desktop-kit-snippet.sh --tag desktop-v<version> --dir /tmp/desktop-v<version> --pubkey agent/internal/kit/keys/minisign.pub
   ```
   It runs `scripts/release-sign.sh verify` first (the minisign signature of `SHA256SUMS`, then every file), then prints the `kit-desktop` item with the url, sha256 and size filled in.
2. Paste the printed item into `catalog/ark.yaml` in the kit section, replacing the previous `kit-desktop` item if there is one. Keep the item `kind: kit`, `survival_tier: 1`, `version_policy: pinned`, `checksum: pinned`.
3. First bump only: add `kit-desktop` to the `include` list of every profile that lists the other `kit-*` items (see `catalog/profiles/`), and update the "Not here yet: kit-desktop" sentence in the header comment of `catalog/ark.yaml` and the "What `catalog/ark.yaml` holds today" paragraph of `docs/specs/rebuild-kit.md`.

## Checks
- [ ] The tag, both asset names and both sha256 values come from `scripts/release/desktop-kit-snippet.sh` run with `--pubkey` (paste its last lines below, **redacted** if anything else is shown)
- [ ] `aiagent ctl catalog lint catalog/` passes (`cd agent && go run ./cmd/aiagent ctl catalog lint ../catalog`)
- [ ] `go test ./internal/kit/... ./internal/catalog/...` passes (from `agent/`)
- [ ] The release is published (not a draft) and the `.deb` URL in the item opens
- [ ] The catalog version (`CATALOG_VERSION`) is bumped if the repository's catalog rules ask for it for a content change, and `scripts/check-redaction.sh` passes
- [ ] Docs: `docs/runbooks/06-rebuild-a-node-offline.md` names the right file pattern (it does not hard-code a version)

## Offline safety
- [ ] No new runtime internet dependency; ark sync downloads the two files while online and keeps the previous verified copy until the new one verifies
- [ ] Nothing here can delete the last verified copy of content

## Redaction
- [ ] `scripts/check-redaction.sh` passes; no IPs, tokens, keys, zone IDs, emails
