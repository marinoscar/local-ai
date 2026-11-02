# ADR-0006: Offline nodes are standalone. There is no LAN mesh

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
Without internet, Tailscale can't coordinate new connections. LAN discovery (mDNS) would let a laptop use the 128 GB node's big model, but it adds discovery, auth and failure modes in exactly the scenario where simplicity matters most.

## Decision
- In `offline` and `survival` modes every node is **fully standalone**. It uses only its own models and content, and exposes nothing on any network interface.
- No mDNS, no LAN API and no peer discovery.

## Consequences
- **Good:** the smallest possible attack surface and failure surface. Each node's readiness is self-contained and testable.
- **Cost:** a small laptop can't borrow a big node's model offline. Mitigation: profiles give every node a capable general model plus vision and speech. The big node is itself a standalone ark.
- **Revisit** only with a new ADR if needed. A future LAN mode would be a separate mode, `lan`, and must not change `offline`/`survival` semantics.

## Alternatives considered
- **mDNS mesh with shared node keys:** rejected for complexity.
- **Self-hosted Headscale on a node:** rejected for complexity, and it still needs one coordinator node alive.
