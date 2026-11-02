# ADR-0003: Cloud → node control goes through outbound pulls on the EAB worker-node plane

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
ai.marin.cr must control every node: load models, sync, rotate keys, read status. The nodes sit behind home NAT and are reachable only over Tailscale. They must keep working when the cloud is gone, and a compromised cloud must not get a shell on every node. EnterpriseAppBase already ships a **worker-node plane** (`docs/specs/worker-nodes.md` in EAB) with these pieces:
- `nod_` credentials confined to `/api/nodes/*`,
- idempotent `register`,
- `heartbeat` with vitals and capabilities,
- `claim`, `renew`, `result` and `failure` with leases and claim tokens,
- admin fleet UI, derived health and immediate revocation.

## Decision
- The agent is an **EAB worker node**. Using its `nod_` credential, it registers, heartbeats every 15 s with a Local AI state snapshot, and long-polls `claim` for **node-eligible job types** named `localai.*`. It reports through `result` or `failure`.
- Each cloud action is an EAB job targeted at one node. Its type comes from a closed set, with a Zod schema on the server and a strict decoder on the agent.
- Rich node state (loaded models, catalog state, READY.json, mode, services) goes in an app-owned state endpoint **under `/api/nodes/`**, so the `nod_` route allowlist covers it (cloud-app.md §3.6). If EAB doesn't let an app mount routes there, file an EAB *seam request* and temporarily carry the state in heartbeat `capabilities`.
- The cloud **never** connects to the agent. The only cloud-initiated inbound traffic to a node is inference through nginx/the gateway to llama-server.

## Implementation note (2026-11-02)
This note refines how the decision is implemented; the decision itself (outbound pulls only, closed job types, `nod_` credential, no inbound ports) is unchanged.
- EAB jobs cannot be targeted at a single node today, and EAB node routes are scoped to the credential's **owner**, not to one node. Until EAB ships the seams, Local AI commands live in an **app-owned `localai_commands` table** and are claimed at `/api/nodes/{id}/localai/commands/{claim,renew,result,failure}`. The field names and the 409 semantics mirror EAB's claim protocol, so the agent reuses its EAB client with another path prefix ([cloud-app.md](../specs/cloud-app.md) §3.6, §4.4).
- The handshake `GET /api/nodes/{id}/localai/config` answers `commandPlane: "localai"`; the agent then claims from the app routes (long poll, 25 s). EAB's own `/api/nodes/{id}/claim` stays as the future `commandPlane: "eab"` mode ([agent.md](../specs/agent.md) §4.8.6).
- A binding guard ties each `nod_` credential to exactly one Local AI node ([cloud-app.md](../specs/cloud-app.md) §4.2).
- Seam requests to EAB: **SR-1** targeted jobs, **SR-2** credential → node binding, **SR-3** `appctl deploy --app-dir` ([cloud-app.md](../specs/cloud-app.md) §3.12). When SR-1 and SR-2 ship, the cloud switches `commandPlane` to `eab`.
- The rich state travels as the strict `LocalaiStateSnapshotV1` (with `boot_id` and `seq`) on `POST /api/nodes/{id}/localai/state`, not as raw agent API bodies; heartbeats carry only EAB vitals.

## Consequences
- **Good:** there are no new inbound ports or firewall rules. Revocation, health, the fleet UI and auditing come from EAB.
- **Good:** offline mode just means the agent stops polling.
- **Cost:** command latency is one poll interval (≤ 3 s). That's acceptable.
- **Cost:** the Go agent must implement the EAB node protocol faithfully, including claim tokens, lease renewal and the 409 semantics. Conformance tests use a fake server built from the EAB spec.

## Alternatives considered
- **WebSocket from the agent to a custom endpoint:** lower latency, but it duplicates EAB auth, health and fleet features.
- **The cloud calling the agent API over the tailnet:** needs inbound exposure of the agent and gives the cloud direct control. Rejected for security.
- **SSH from the VPS:** this hands the cloud a full shell. Rejected.
