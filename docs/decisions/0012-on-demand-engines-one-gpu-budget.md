# ADR-0012: On-demand engines share one GPU budget, switched by named workloads

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** owner (decisions delegated for epic E11, issue #506)

## Context
A node's GPU is shared. Until now one program owned it: the llama router ([ADR-0002](0002-llama-router-mode.md)), with whisper as a small, reserved guest ([companion-services.md](../specs/companion-services.md) §4.2.3). The fit rules of [llama-router.md](../specs/llama-router.md) §4.3.3 count the router's models and subtract what other programs already hold (`G_others`). The load orchestrator (§4.4) serializes every change under one lock, `modelOpsMu`, and rolls back on failure.

Music generation ([ADR-0011](0011-python-inference-engines.md)) adds a second GPU engine that needs most of a consumer card while it runs. On an 8 to 16 GB node, a coding model and the music model do not fit together. A person should not have to know which model belongs to which engine, unload one by hand and load the other. They want to say "this computer makes music now", and later "back to coding".

Survival mode ([modes.md](../architecture/modes.md) §2.2) must keep working whatever ran before: it must get its models loaded even when the music engine is busy or broken.

## Decision
1. **Every GPU-using engine is a loadable unit behind an engine adapter.** The agent has one adapter per engine: `llama` for the router, `acestep` for music, later GPU text-to-speech. An adapter knows how to start its engine for a model, tell when it is ready, stop it and measure what it holds. A catalog model names its engine (`engine:` in [catalog.md](../specs/catalog.md) §2). A model whose engine is not installed on this node is not loadable (`LAI-LLM-043`).
2. **One GPU budget, one fit check.** The fit of llama-router.md §4.3.3 counts every loaded model of **every** engine. Loading any model may evict the least recently used loaded models of **any** engine, with the same tie-breaks and the same `unload_others` switch. `modelOpsMu` serializes all loads and unloads of all engines; there is never a second lock.
3. **Same state machine, same rollback.** A load through any adapter follows the existing orchestration: check, lock, plan, evict, load, and on failure clean up and restore what was evicted. Adapter failures have their own codes: start failed `LAI-LLM-040`, not ready in time `LAI-LLM-041`, did not free GPU memory `LAI-LLM-042`. A rollback is never rolled back again.
4. **Named workloads are the human-facing switch.** A workload is a named set of model ids (`coding`, `chat`, `speech`, `music`, ...), declared per profile (`workloads:` in catalog.md §4) and overridable per node in `node.yaml`. Activating one is a **diff**: unload every loaded model that is not in the set, then load the set, under the same lock. A failure restores the previous set (`LAI-LLM-052`). People see labels ("Music", "Coding") and one button; model ids stay for experts.
5. **Survival always wins.** Survival entry stops every non-survival engine (music first) **before** it loads the survival models, and never fails because of it: a stop that fails is logged (`LAI-LLM-042`) and survival goes on. Music is never started in survival. A workload change in survival is refused unless forced (`LAI-LLM-053`), and a workload never unloads a survival model.
6. **Remote control only through existing command paths.** The cloud switches workloads and asks for songs with command jobs on the outbound link ([ADR-0003](0003-outbound-only-cloud-link.md)): `localai.workload.activate` and `localai.music.generate`. The desktop uses the agent's local UI and its allowlisted Lab proxy ([ADR-0004](0004-desktop-thin-shell.md)). No new inbound port, and no direct access to the engine's port.

## Consequences
- **Good:** one rule for "does it fit", one lock and one rollback, whatever the engine. The drill and the tests exercise one state machine.
- **Good:** a non-expert switches the purpose of a computer with one button in the node UI, the desktop, the cloud or the `aictl` menu ("Switch what this computer does").
- **Cost:** an engine that starts slowly (a PyTorch program loads in tens of seconds) makes a workload switch slow. The switch shows progress (`workload.progress`) and can be waited for or left running.
- **Cost:** the engine adapters must report their memory honestly. Until the spike (#505) measures ACE-Step, its `requirements.min_vram_mib` is the declared need, and `G_others` keeps measuring the real use.
- **Risk:** an engine that does not free GPU memory when stopped. The adapter waits, then kills the unit, then raises `LAI-LLM-042`; the planner measures `G_others` again before loading anything else.

## Alternatives considered
- **Separate budgets per engine (a fixed VRAM split):** rejected. It wastes most of a small card most of the time, and an 8 GB node could run neither engine well.
- **Let engines fight it out (start whatever is asked and rely on out-of-memory errors):** rejected. It produces crashes instead of messages a person can act on, and it can evict the survival model.
- **A second lock for music:** rejected. Two locks over one GPU can deadlock or double-book memory.
- **Workloads only in the cloud:** rejected. An offline node must switch too ([ADR-0006](0006-standalone-offline.md)); the definition lives in the catalog and `node.yaml`, and every UI calls the same agent route.
