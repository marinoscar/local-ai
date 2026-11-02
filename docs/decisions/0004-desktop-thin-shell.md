# ADR-0004: ai-desktop is a thin Electron shell over the agent's local UI

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
A non-expert needs a desktop app to control the local node and survive offline. It should also be able to connect to the cloud. kvox-desktop shows the house pattern: Electron, electron-builder, hardened fuses, loopback-only egress and deb/AppImage builds. But kvox-desktop embeds a whole NestJS server and database, because it owns its data. Here the **agent** already owns all node state and serves a local UI.

## Decision
- ai-desktop is an **Electron shell with no domain logic and no database**. It:
  1. reads `/etc/localai/agent-token` (the user is in `localai-operators`) and opens a session with the agent,
  2. shows the agent's local UI (`http://127.0.0.1:7070/`) in a hardened BrowserWindow,
  3. adds the tray icon, autostart, desktop launcher, the native "Copy API key" clipboard action, and opening the WebUI, Kiwix and maps in windows,
  4. in **Connected** mode, links to ai.marin.cr through EAB device authorization (RFC 8628) and shows a "Lab" tab that embeds the cloud UI for the whole lab.
- The local UI is built once (React, in `packages/ui` + `agent/ui`). It is used by the agent, by ai-desktop and, in shared components, by the cloud.
- If Electron breaks, **any browser** at `http://127.0.0.1:7070` provides the same UI. This is by design.

## Consequences
- **Good:** one UI to build and test, and the desktop is small and hard to break. Survival doesn't depend on Electron.
- **Cost:** desktop-only niceties (tray, notifications) need a small preload IPC surface. It is kept minimal and allowlisted.

## Alternatives considered
- **Full kvox-desktop clone with its own server and DB:** duplicates the agent and gives two sources of truth.
- **Tauri:** a second runtime toolchain, and the kvox experience favoured Electron (kvox ADR-0002).
- **Browser only, no desktop app:** a non-expert needs an icon to double-click plus tray status. Kept as the fallback.
