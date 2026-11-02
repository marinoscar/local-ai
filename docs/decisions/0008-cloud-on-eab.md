# ADR-0008: The cloud app is built on EnterpriseAppBase packages

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
ai.marin.cr needs Google sign-in, RBAC, an encrypted credential store, a job queue, worker nodes, device authorization for the desktop, an admin UI shell, deployment to the existing VPS proxy, and observability. The owner's EnterpriseAppBase (EAB) provides all of these as published `@marinoscar/platform-*` packages. kvox is the precedent for an app built on them.

## Decision
- `apps/cloud` is generated from the EAB starter:
  `node scripts/new-project.mjs create --dir <local-ai>/apps/cloud --name "Local AI" --repo marinoscar/local-ai`, run from the EAB checkout. The cloud app lives **inside this monorepo** under `apps/cloud`.
- Its platform package versions are pinned exactly.
- Domain code (nodes view, commands, catalog, gateway) is app-owned under `apps/cloud/apps/api/src/localai/` and `apps/cloud/apps/web/src/localai/`. Platform code is never edited. Missing hooks are filed upstream as EAB seam requests.
- Deployment uses `appctl deploy` to the VPS behind the shared `/opt/infra/proxy`, at `ai.marin.cr`.

## Consequences
- **Good:** auth, RBAC, jobs, nodes, credentials, device auth, the settings hub, doctor checks and deploy come almost for free.
- **Cost:** the app follows EAB conventions (`AGENTS.md` inside `apps/cloud`, issue-first, `pp:migration` label). An EAB layout nested in the monorepo needs care with the CI paths. Note: the generator runs `git init`. Delete the nested `.git` it creates, since `apps/cloud` belongs to this repo.

## Alternatives considered
- **A custom Express/Fastify app:** re-implements auth, RBAC and jobs badly.
- **Extend Open WebUI:** wrong tool. It's a chat UI, not a fleet controller.
