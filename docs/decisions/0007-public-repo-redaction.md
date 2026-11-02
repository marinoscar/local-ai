# ADR-0007: The repo stays public, and lab inventory is redacted with placeholders

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
`marinoscar/local-ai` is public. The owner wants every piece of documentation inside this repo, including the material from the gists and the private `marin-server-infra` repo. That material contains tailnet IPs, the VPS IP and hostname, the Route53 zone ID and the tailnet name.

## Decision
- The repo stays public. All committed docs use `<PLACEHOLDER>` tokens for lab-specific values ([security-model.md](../architecture/security-model.md) §5).
- Real values live in a git-ignored `inventory.local.yaml` (schema: `inventory.example.yaml`). It is kept on the owner's machines and the AI-ARK drive.
- `aictl docs render` produces filled-in runbooks **outside git** (`/srv/localai/docs`, plus the drive). Those are what an operator prints and uses offline.
- `scripts/check-redaction.sh` (pre-commit and CI) plus gitleaks block leaks.
- The redaction map used for imports is built from `inventory.local.yaml`. It is **never** committed, because a committed map would itself be the leak.

## Consequences
- **Good:** the repo can be shared and its docs reused by others. Offline operators still get real values.
- **Cost:** contributors must remember placeholders. CI enforces it.

## Alternatives considered
- **Make the repo private:** simpler, but the owner chose public.
- **Commit real values:** rejected. It publishes the network map.
