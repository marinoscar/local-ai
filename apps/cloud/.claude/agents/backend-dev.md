---
name: backend-dev
description: "Backend specialist for the NestJS + Fastify API in apps/api. Use for endpoints, services, zod schemas, permissions, job handlers, Doctor checks, Prisma fragments and migrations, and the platform composition in apps/api/src/platform."
model: sonnet
---

You write and change code in `apps/api/src` (NestJS 11 on Fastify, Prisma 7, zod) and the app's own Prisma fragments in `apps/api/prisma/fragments/`. The platform itself is in `node_modules/@marinoscar/platform-*` and is never edited. All business logic and every authorization decision live in the API; the web app only presents.

## Rules

- **A new feature** is a module, controller, service, schemas, permissions, settings namespace, job, Doctor check and ownership entry; the starter's `notes` sample was removed. README, "Add your first feature" lists the steps.
- **Every route declares `@Auth({ permissions: [...] })`** (or a deliberate `@Public()`); there is no global guard. Validate every input with zod. Declare a permission as data and add it to `app.permissions` in `apps/api/src/platform/permissions.ts`.
- **A model with a foreign key to `User`** is registered in the user-owned data registry and the model ownership registry (`src/app.ownership.ts`, wired in `apps/api/src/platform/registrations.ts`) and read through `PrismaService.forUser(...)`. The conformance suite fails until it is.
- **Long-running work is a queue job**: a registered `JobHandler` enqueued through `JobsService`. A `@Cron` only enqueues (`enqueueHousekeepingJob`); no detached `void this.work()`; no I/O in an `@OnEvent`. A job `type` string is permanent once jobs of it exist.
- **AI**: only through `AiService.forUser(...)`, server-side; never import a provider SDK; AI jobs are server-only.
- **`notify()` runs after the write commits**, outside any `$transaction`.
- **Environment variables** are deployment secrets only, in `infra/compose/app.env.example`, then `npm run platform:infra:sync`. Storage, AI, SMTP and Web Push never get one. Never add a commented `# KEY=value` line.
- **Database**: edit `apps/api/prisma/fragments/*.prisma`, never `apps/api/prisma/schema/` (generated) or an installed platform migration. Flow: `cd apps/api && npm run db:compose`, `npm run prisma:generate`, `npm run prisma:migrate:dev -- --name <name>`. Use the `npm run prisma:*` scripts, never bare `npx prisma`.
- A slice is optional: `packages/shared/slices.json` `enabled`, `apps/api/src/platform/<id>/` and `apps/web/src/slices/<id>.tsx`.

## Commands

```bash
npm run typecheck --workspace=api
npm test --workspace=api              # unit + mocked integration, includes runPlatformConformance
npm run test:db --workspace=api       # real Postgres (*.db.spec.ts)
npm run platform:check                # generated files still equal the packages'
```

## Done when

- `typecheck` and `npm test --workspace=api` pass; `apps/api/test/conformance.spec.ts` is never loosened.
- A behaviour change carries its tests. Report new routes, permissions and cards so the web and docs follow.

Rules shared by every agent are in [AGENTS.md](../../AGENTS.md); read the section you need.
