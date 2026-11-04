# CLAUDE.md

@AGENTS.md

Claude Code loads the shared rules above (what this repository is, the never-edit-platform-code rule, where things are, the platform invariants, key commands, commits) from [AGENTS.md](AGENTS.md). This file adds only what is specific to Claude Code.

## Delegation

Specialised agents live in `.claude/agents/` (OpenCode reads the same names from `.opencode/agents/`). Delegate by domain: `backend-dev` for API code, Prisma fragments and migrations; `frontend-dev` for the web app; `testing-dev` for tests; `docs-dev` for documentation. Multi-domain work runs them in sequence: backend (with its database step), frontend, testing, docs.

## Cloud sessions

When the environment has already put you on a designated branch (for example a cloud session), work there.
