# The worker image: this app's CLI running `node start --headless`
# (infra/compose/worker.compose.yml). Its <CLI>_* variables come from the
# compose file and .env.worker, so nothing here names the CLI.
FROM node:24-alpine AS base
WORKDIR /app

FROM base AS deps
COPY package.json package-lock.json ./
COPY apps/api/package.json ./apps/api/
COPY apps/web/package.json ./apps/web/
COPY apps/cli/package.json ./apps/cli/
COPY packages/shared ./packages/shared/
RUN npm ci --workspace=cli --include-workspace-root --ignore-scripts

FROM base AS build
COPY --from=deps /app ./
COPY tsconfig.base.json ./
COPY apps/cli ./apps/cli
WORKDIR /app/apps/cli
RUN npm run build

FROM base AS production
COPY --from=deps /app ./
COPY --from=build /app/apps/cli/dist ./apps/cli/dist
RUN mkdir -p /var/lib/worker && chown -R node:node /var/lib/worker /app
USER node
VOLUME ["/var/lib/worker"]
ENTRYPOINT ["node", "/app/apps/cli/dist/cli.js", "node", "start", "--headless"]
