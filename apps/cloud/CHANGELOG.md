# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- VPS deploy no longer fails at "Build images" looking for `apps/stack-agent`: the overlay `infra/compose/app.vps.no-stack-agent.compose.yml` turns off the platform's stack-agent container, which this app does not ship (#450).

## [0.1.0] - 2026-11-04

### Added

- Initial project, started from the platform starter.
