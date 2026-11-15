# ADR-0015: A Python engine's runtime is a kit wheelhouse that ark sync fills, with a pinned standalone Python

- **Status:** Accepted. Supersedes the delivery part of [ADR-0012](0012-python-inference-engines.md) decision 2 (the release-asset "wheel bundle" and the system Python); the rest of ADR-0012 stands.
- **Date:** 2026-11-15
- **Deciders:** owner (decision delegated for issue #511)

## Context
[ADR-0012](0012-python-inference-engines.md) said a Python engine's venv is built offline from "a vendored wheel bundle in the rebuild kit (`kit/engines/<engine>/<version>/<engine>-<version>-<backend>.tar.zst`)", made by CI and published as a GitHub release asset, and installed on the system Python. Two facts from the spike (#505) break that:
1. **Size.** The CUDA wheels of ACE-Step 1.5 are about 4.4 GB (cuDNN, cuBLAS, NCCL and the other CUDA libraries, torch 2.10.0+cu128); the installed venv is about 8.1 GB. A GitHub release asset is at most 2 GiB per file, so the bundle cannot be published as one asset, and splitting it would be a format of our own to maintain and verify.
2. **Python version.** ACE-Step needs Python 3.11 or 3.12; Ubuntu 26.04 ships 3.14. The spike ran a python-build-standalone CPython 3.12.16.

The kit already has a source type that is filled on an online node from pinned inputs and is not a release asset: the git mirrors (`git_mirror`, [ark-sync.md](../specs/ark-sync.md) §4.9.6), and the apt closures download many verified files.

## Decision
1. **A wheelhouse, filled by ark sync.** The kit item `kit-engine-<engine>-<backend>` has a new source type, `pip_wheelhouse` ([rebuild-kit.md](../specs/rebuild-kit.md) §3.2.2, [ark-sync.md](../specs/ark-sync.md) §4.9.11). On an online node ark sync downloads, through the sync downloader (resume, sha256, quarantine), the wheels of a hashed lock and the pinned Python into `kit/engines/<engine>/<version>/`, builds the few wheels no index has (ACE-Step, nano-vllm, our shim) offline from pinned sources, and seals the folder like any other kit item. It is verified, budgeted, copied to the drive and checked by `aictl kit verify` like the rest of the kit.
2. **Pinned inputs in the repository.** `engines/<engine>/requirements-<backend>.in` (every package `==`) and the generated `requirements-<backend>.lock` (one wheel and its sha256 per package, from the index's own digest, written by `scripts/release/lock-acestep.sh`); the item in `catalog/ark.yaml` pins the lock's sha256, the Python tarball's sha256, the indexes and the git commits. Nothing is resolved at sync or install time: a wheel is found by its sha256, and a lock with an unresolved package is refused.
3. **A pinned standalone Python.** The runtime runs on a pinned python-build-standalone CPython (3.12.16, release 20261110, the `install_only` tarball) carried in the wheelhouse and unpacked to `/opt/localai/<engine>/<version>/python`, not on the system Python.
4. **The install is unchanged in spirit.** `ensure` (row `engine.acestep`) re-hashes the wheelhouse, unpacks the Python, makes the venv with it, runs `pip install --no-index --find-links <wheels> --require-hashes -r <lock>`, the self-test, writes `BUILD.json` and only then moves `current`. The venv is built at its final path (a venv cannot be moved) behind an `.incomplete` marker.

## Consequences
- **Good:** no 2 GiB limit, no CI job that downloads 4.4 GB per release, no format of our own; the wheelhouse is exactly the files pip installs, each verified against a digest pinned in the repository.
- **Good:** the Python version is pinned with the wheels, so the runtime does not change when Ubuntu's Python does, and an offline rebuild from the drive is byte-identical in its inputs.
- **Good:** the kit item reuses the sync engine (resume, quarantine, budget, local copy to the drive, verification) and `aictl kit status`.
- **Bad:** the first fill needs an online node with the space (about 4.5 GB in the kit plus about 8.5 GB for the runtime) and access to PyPI, the PyTorch index and GitHub. A node that has never been online cannot make music until a drive that has the wheelhouse is plugged in.
- **Bad:** the three wheels sync builds are not byte-pinned in the repository: their sha256 is computed by sync and recorded in the kit's lock and `SHA256SUMS`. Their sources are pinned (a git commit, the shim's tree hash in the lock), and the build runs with no network.
- **Bad:** the lock must be regenerated with internet when a pin changes (`lock-acestep.sh`), and the shim's tree hash after every shim edit (`--shim-only`); a Go test fails when they disagree.

## Alternatives considered
- **Split the bundle into several release assets under 2 GiB:** rejected. A format of our own to build, reassemble and verify, and CI would still download 4.4 GB per release.
- **Host the bundle elsewhere (object storage, a large-file host):** rejected. A new external service the ark depends on, against [ADR-0006](0006-standalone-offline.md) and the public-repo rule ([ADR-0007](0007-public-repo-redaction.md)) for credentials.
- **Keep the system Python:** rejected. Ubuntu 26.04's 3.14 cannot run ACE-Step.
- **Conda or a packed environment (`conda-pack`):** rejected as in ADR-0012: a second package manager in the kit.
