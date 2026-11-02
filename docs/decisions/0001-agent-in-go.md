# ADR-0001: The node agent and CLI are one static Go binary

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
Each node needs a long-running agent: services, models, sync, modes, local UI and the cloud link. It also needs a CLI. The agent must keep working **for years without internet**, and it may have to be reinstalled on a freshly wiped machine from the ark drive alone. The cloud and desktop apps use TypeScript (EnterpriseAppBase, Electron).

## Decision
- The agent is written in **Go**. It builds with `CGO_ENABLED=0` into **one static binary**, `aiagent`, for linux/amd64 and linux/arm64.
- The same binary acts as the CLI when it is invoked as `aictl` (multi-call through `argv[0]`, with an `aiagent ctl …` fallback).
- The local UI (React + Vite build) is embedded with `go:embed`.
- Dependencies are vendored (`go mod vendor`), and the rebuild kit carries a Go toolchain tarball, so the agent can be rebuilt offline.
- The REST API is described by an OpenAPI 3.1 document, `agent/api/openapi.yaml`. It is written first and validated against the handlers in tests. TS types for the cloud and desktop are generated from it.

## Consequences
- **Good:** there is no runtime to install (no Node, no Python), startup is instant, and memory use is tiny. It runs on a broken system as long as the kernel boots.
- **Good:** a single file to copy makes manual recovery trivial (`cp /media/*/AI-ARK/localai/kit/bin/aiagent-linux-amd64 /opt/localai/bin/aiagent`).
- **Cost:** a second language in the repo. The EAB worker-node protocol must be implemented as a Go client instead of reusing `appctl node`. Mitigation: the protocol is HTTP+JSON and documented in EAB `docs/specs/worker-nodes.md`, and the conformance tests run against a fake server.

## Alternatives considered
- **TypeScript/Node agent (reusing `appctl node`):** this needs a Node runtime on every node, so the offline rebuild must also bootstrap Node. The npm dependency tree is too fragile for a decade-long ark.
- **Bash scripts:** quick to write, but they can't safely implement resumable verified sync, a state machine, a REST API or a local UI.
- **Python:** same runtime/dependency fragility as Node.
