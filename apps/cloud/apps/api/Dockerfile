# The API image. Built from the repository root (infra/compose/base.compose.yml:
# context ../.., dockerfile apps/api/Dockerfile). The platform comes from the
# registry like every other dependency: there is no package source here.
FROM node:24-alpine AS base
WORKDIR /app
# pg_dump/pg_restore for the database-backup slice once it is enabled.
RUN apk add --no-cache openssl postgresql17-client

FROM base AS deps
COPY package.json package-lock.json ./
COPY apps/api/package.json ./apps/api/
COPY apps/web/package.json ./apps/web/
COPY apps/cli/package.json ./apps/cli/
COPY packages/shared ./packages/shared/
RUN npm ci --workspace=api --include-workspace-root
COPY apps/api/prisma ./apps/api/prisma/
COPY apps/api/prisma.config.ts ./apps/api/
RUN cd apps/api && DATABASE_URL="postgresql://build:build@localhost:5432/build" npx prisma generate

# The catalog checker. The cloud lints catalogs with the REAL `aictl catalog lint --json` (docs/specs/cloud-app.md
# section 4.6), never a copy of its rules: this stage downloads the static `aiagent` of the matching agent release
# (`node-v<AIAGENT_VERSION>`), checks it against the release's SHA256SUMS and fails the build on any mismatch.
# `aiagent` is a multi-call binary chosen by argv[0], so `aictl` is a symlink to it. Only the production image carries
# it; the development image has none and a lint then ends as LAI-CLD-033.
FROM base AS aiagent
ARG AIAGENT_VERSION
ARG AIAGENT_RELEASE_REPO=marinoscar/local-ai
ARG TARGETARCH
RUN set -eu; \
    test -n "$AIAGENT_VERSION" || { echo "AIAGENT_VERSION is not set: build the API image with the agent release version (infra/compose/app.env.example)" >&2; exit 1; }; \
    case "${TARGETARCH:-amd64}" in amd64|arm64) arch="${TARGETARCH:-amd64}" ;; *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; esac; \
    apk add --no-cache curl; \
    base="https://github.com/${AIAGENT_RELEASE_REPO}/releases/download/node-v${AIAGENT_VERSION}"; \
    mkdir -p /opt/localai/bin /tmp/aiagent; cd /tmp/aiagent; \
    curl --proto '=https' --tlsv1.2 -fsSL -o "aiagent-linux-${arch}" "${base}/aiagent-linux-${arch}"; \
    curl --proto '=https' --tlsv1.2 -fsSL -o SHA256SUMS "${base}/SHA256SUMS"; \
    line="$(grep "  aiagent-linux-${arch}\$" SHA256SUMS)"; test -n "$line" || { echo "SHA256SUMS has no entry for aiagent-linux-${arch}" >&2; exit 1; }; \
    echo "$line" | sha256sum -c -; \
    install -m 0755 "aiagent-linux-${arch}" /opt/localai/bin/aiagent; \
    ln -s aiagent /opt/localai/bin/aictl; \
    /opt/localai/bin/aictl catalog lint --help >/dev/null

FROM base AS development
COPY --from=deps /app ./
COPY tsconfig.base.json ./
COPY apps/api ./apps/api
WORKDIR /app/apps/api
EXPOSE 3000
CMD ["npm", "run", "start:dev"]

FROM base AS build
COPY --from=deps /app ./
COPY tsconfig.base.json ./
COPY apps/api ./apps/api
WORKDIR /app/apps/api
RUN npm run build

FROM base AS production
ENV NODE_ENV=production
# The catalog checker (see the `aiagent` stage); LOCALAI_AICTL_PATH is the path the lint job runs.
COPY --from=aiagent /opt/localai /opt/localai
ENV LOCALAI_AICTL_PATH=/opt/localai/bin/aictl
COPY --from=deps /app ./
COPY tsconfig.base.json ./
COPY --from=build /app/apps/api/dist ./apps/api/dist
# The seed (`npm run prisma:seed`) reads the permission catalog and settings
# registry from src/, so the image carries them with prisma/.
COPY apps/api ./apps/api
WORKDIR /app/apps/api
EXPOSE 3000
CMD ["node", "dist/main.js"]
