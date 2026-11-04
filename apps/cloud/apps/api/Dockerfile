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
COPY --from=deps /app ./
COPY tsconfig.base.json ./
COPY --from=build /app/apps/api/dist ./apps/api/dist
# The seed (`npm run prisma:seed`) reads the permission catalog and settings
# registry from src/, so the image carries them with prisma/.
COPY apps/api ./apps/api
WORKDIR /app/apps/api
EXPOSE 3000
CMD ["node", "dist/main.js"]
