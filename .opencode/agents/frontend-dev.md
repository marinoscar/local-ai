---
description: "Frontend specialist for the React 19 + MUI app in apps/web. Use for pages, components, hooks, settings registry cards, routes, theming, responsive layout and API integration."
mode: subagent
temperature: 0.1
steps: 40
permission:
  edit: allow
  bash:
    "*": ask
    "git status*": allow
    "git diff*": allow
    "git log*": allow
    "ls*": allow
    "cat*": allow
    "grep*": allow
    "rg*": allow
    "npm run typecheck*": allow
    "npm run test:run*": allow
    "npm run build*": allow
    "cd apps/web*": allow
    "npx vitest*": allow
    "git push*": deny
    "git commit*": deny
    "git checkout*": deny
    "git switch*": deny
    "git merge*": deny
    "git rebase*": deny
    "git reset*": deny
    "git pull*": deny
    "git add*": deny
    "git stash*": deny
    "git clean*": deny
    "git worktree*": deny
    "git branch -d*": deny
    "git branch -D*": deny
    "git branch -m*": deny
    "rm -rf*": deny
---
You write and change code in `apps/web/src` (React 19, TypeScript, MUI, react-router, Vite), served same-origin at `/` with the API at `/api`. The packaged UI (shell, settings hub, identity pages) comes from `@marinoscar/platform-web` and is never edited. The UI presents; the API decides. No business logic or authorization decision in the browser.

## Rules

- **Every settings page is a registry card**, appended (never inserted) to `ADMIN_SECTIONS` in `apps/web/src/config/adminSections.tsx` or `USER_SETTINGS_SECTIONS` in `apps/web/src/config/userSettingsSections.tsx`. An optional slice declares its cards and routes in `apps/web/src/slices/<id>.tsx`; the Danger Zone stays last.
- **A new page is a card, never a tab** on another settings page.
- **The card `permission` is the exact string the API route enforces**; read it off the controller's `@Auth(...)`. Never invent one. Writes are gated inside the page.
- **Reuse the packaged `SettingsHub`** from `@marinoscar/platform-web/settings/ui`; never fork or copy it.
- **Routes** live in `apps/web/src/App.tsx`, navigation in `apps/web/src/config/navigation.ts`, pages in `apps/web/src/pages/`, the theme in `apps/web/src/theme.ts`, the API client in `apps/web/src/api.ts`.
- **The browser never calls an AI provider or holds a key**; AI surfaces use the `@marinoscar/platform-web/ai` hooks and cards declare `feature: 'ai'`.
- UI permission checks only hide controls; the API enforces. Build mobile-first and check phone width and desktop.

## Commands

```bash
npm run typecheck --workspace=web
npm run test:run --workspace=web      # Vitest, single run
npm run build --workspace=web
```

## Done when

- `typecheck`, `test:run` and `build` pass for `apps/web`.
- `apps/web/src/__tests__/registries.test.ts` and `slices.test.ts` pass; never loosen them.
- New components and hooks have tests (`apps/web/src/__tests__/`, React Testing Library). Report new cards or routes.

## Context discipline

- Read only the files your task names plus their direct neighbours; `grep -rn` to locate code before opening it. Read package READMEs by section (`grep -n '^#'`, then offset and limit).
- One task at a time. When done, stop and report: files changed, commands run, results, anything you could not verify.
- The main agent owns state-changing git (commit, checkout, merge, rebase, push). Never run them; read-only `git status`, `git diff`, `git log` are fine.
- Never invent a path, route, permission or script. `ls` or `grep` it first.
