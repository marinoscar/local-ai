# ADR-0015: The music runtime is synced file by file, and brings its own Python

- **Status:** Accepted · Supersedes decision 2 of [ADR-0012](0012-python-inference-engines.md) (how the venv's files reach the kit, and which Python builds it)
- **Date:** 2026-11-14
- **Deciders:** owner (issue #511)

## Context
ADR-0012 said the music runtime is built offline from one wheel bundle (`acestep-<version>-<backend>.tar.zst`) published on a GitHub release, on the node's own Python. The spike (#505) and the first install on ai-n2 found two problems:

1. **The bundle does not fit a release.** The CUDA runtime is about 7 GB of wheels (torch with its NVIDIA libraries is most of it). A GitHub release file can be at most 2 GiB, so the bundle would have to be split and joined again, and the repo would have to host several GB that PyPI and the PyTorch index already host.
2. **The node's Python is too new.** ACE-Step 1.5 requires Python 3.11 or 3.12; Ubuntu 26.04, the node OS, ships 3.14 and has no 3.12 package.

## Decision
1. **Each file is a kit file.** The kit item `kit-engine-acestep-<backend>` (rebuild-kit.md §3.1, at `kit/engines/acestep/<backend>/`) lists **every wheel at its origin** (PyPI, the PyTorch wheel index) with its pinned sha256, like any other `url` kit item. `aictl ark sync` downloads and verifies them as it does every other file. Only what no index has is published on the release `acestep-<version>` of this repo: the hashed `requirements.lock` and the wheels built from the pinned ACE-Step commit (ace-step, nano-vllm) and from `engines/acestep` (the shim). Every file is far below 2 GiB.
2. **The runtime brings its own Python.** The item also carries a pinned python-build-standalone "install_only" tarball of the Python the runtime needs (3.12). The install unpacks it as `/opt/localai/acestep/<version>/venv` (it is relocatable) and installs the wheels into it with `pip install --no-index --require-hashes`. Without that tarball the install falls back to `python3 -m venv`, as before.
3. **Unchanged from ADR-0012:** nothing is resolved or downloaded at install time; the lock has a hash per wheel; the install is offline, idempotent and moves `current` only after the self-test passes, which now imports ACE-Step and torch.

## Consequences
- **Good:** no file to split, nothing large hosted by this repo, the same verification and resume as every other kit file, and the runtime works on any Ubuntu whatever its Python.
- **Good:** `scripts/release/build-acestep-kit.sh` writes the catalog's file list from the spike's pins and checks it with a real offline install before anything is published.
- **Bad:** the item has about 150 files in `catalog/ark.yaml`, and its origins are PyPI and download.pytorch.org as well as GitHub. A file removed upstream stops new syncs (never an installed node, nor a drive that already has it); the release workflow is the place to notice.
- **Bad:** a second CPython on the node (about 70 MB) to keep pinned and updated with the lock.

## Alternatives considered
- **One bundle split into parts under 2 GiB:** works, but hosts 7 GB per version on this repo's releases, and the parts are useless one by one.
- **`pip install` from the internet at install time:** breaks the offline rebuild (AGENTS.md rule 3, ADR-0006).
- **Python 3.12 from a PPA or a source build on the node:** a PPA is a third apt origin for one program and has no 26.04 build yet; compiling CPython on each node is slow and needs its build dependencies in the kit.
