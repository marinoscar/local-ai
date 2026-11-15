# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Every `laictl deploy update` bumps the app's release version (`apps/api`, `apps/web`, `packages/shared`; the list is `packages/shared/versioned-manifests.json`). The CLI is no longer bumped by a deploy: it keeps its own version, raised with every CLI change and checked in CI (`scripts/check-cli-version-bump.sh`). CLI 0.1.1.
- Republishing `deploy/cloud` keeps the version a deploy pushed onto the branch (`scripts/carry-deploy-version.mjs`), so the version never moves backwards (#574).

### Fixed

- OpenAI GPT-6 models (`gpt-6-sol`, `gpt-6-astra`, `gpt-6.1-sol`, `gpt-6.1-terra`, and later generations) are classified like GPT-5 instead of `unclassified` with no capability, so they can be enabled (EnterpriseAppBase #1155). The app moves to the platform packages 0.1.0-next.31, with two platform migrations (notifications state, storage migrations) and the generated infra files re-synced. CLI 0.1.3.
- The AI usage card (Settings > AI, `GET /api/ai/usage/me`) no longer fails with "this.prisma.forOrg is not a function": `PrismaService` implements `forOrg`, which the platform's AI, storage and credentials slices call through `PLATFORM_PRISMA` (EnterpriseAppBase #1155). A test pins every method of the port.
- `laictl deploy update` no longer stops at "Look for a new revision" when the repository moves a tag (`catalog-stable`): the CLI is on `@marinoscar/platform-cli` 0.1.0-next.30, which fetches with `--force` (marinoscar/EnterpriseAppBase#1151). CLI 0.1.2; the generated infra files are re-synced to `@marinoscar/platform-infra` 0.1.0-next.30.
- The web image build no longer fails when a test imports a file outside `apps/web`: `npm run build` type-checks from the shipped entry points only (`apps/web/tsconfig.build.json`), and cloud-ci builds both images from the flat deploy tree (#574).

- VPS deploy no longer fails at "Build images" looking for `apps/stack-agent`: the overlay `infra/compose/app.vps.no-stack-agent.compose.yml` turns off the platform's stack-agent container, which this app does not ship (#450).

## [0.1.0] - 2026-11-04

### Added

- Initial project, started from the platform starter.
