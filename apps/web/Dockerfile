# The web image. Built from the repository root (base.compose.yml).
FROM node:24-alpine AS base
WORKDIR /app

FROM base AS deps
COPY package.json package-lock.json ./
COPY apps/api/package.json ./apps/api/
COPY apps/web/package.json ./apps/web/
COPY apps/cli/package.json ./apps/cli/
COPY packages/shared ./packages/shared/
# @localai/ui is a workspace of the lock file. In the repository packages/localai-ui is a symlink; the deploy branch
# (scripts/cloud-deploy-branch.sh) replaces it with a real directory, so the install finds its package.json.
COPY packages/localai-ui/package.json ./packages/localai-ui/
RUN npm ci --workspace=web --include-workspace-root

FROM base AS development
COPY --from=deps /app ./
COPY apps/web ./apps/web
WORKDIR /app/apps/web
EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host"]

FROM base AS build
COPY --from=deps /app ./
# The shared UI package is TypeScript source linked into node_modules/@localai/ui (real directory on the deploy branch).
COPY packages/localai-ui ./packages/localai-ui/
COPY apps/web ./apps/web
# `tsc --noEmit` in the build also checks the web tests, and apps/web/src/localai/lab/fixture.ts imports this fixture.
COPY apps/api/test/fixtures/localai ./apps/api/test/fixtures/localai/
WORKDIR /app/apps/web
RUN npm run build

FROM nginx:1.30-alpine AS production
COPY --from=build /app/apps/web/dist /usr/share/nginx/html
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 5173
CMD ["nginx", "-g", "daemon off;"]
