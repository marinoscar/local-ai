# Node operating modes

> **Single source of truth** for the node mode state machine. It is implemented by the agent ([agent.md](../specs/agent.md) §4.2), and surfaced by `aictl mode` ([aictl.md](../specs/aictl.md)), the local UI, the Survival Portal, ai-desktop and the cloud.

Every node is always in exactly one **mode**. The mode is persisted in `/var/lib/localai/state.json` (`mode`), so it **survives reboot**: a node put into survival mode comes back in survival mode. Each committed change is also appended, with its time, to a bounded `mode_history` in the same file (newest 50, 90 days: [agent.md](../specs/agent.md) §3.5), which READY reads to tell a node that was offline on purpose from one that did not sync.

| Mode | One-line meaning | Who uses it |
|---|---|---|
| `online` | Normal lab member: reachable via Tailscale/nginx, controlled by the cloud, syncing nightly | Default for all lab nodes |
| `offline` | Private/standalone: nothing leaves the machine and nothing comes in; local AI still fully usable | Travel, privacy, "pull the plug" testing, internet down but not an emergency |
| `survival` | `offline` + everything needed to survive is started and the Survival Portal is the home screen | Emergency; non-expert operation |

Mode is about **policy**, not about whether the internet actually exists. An `online` node with no internet keeps working; it just reports `connectivity: none` (see §4).

## 1. What each mode does

| Behaviour | `online` | `offline` | `survival` |
|---|---|---|---|
| llama-server router running | yes | yes | yes |
| Models loaded at mode entry | unchanged | unchanged | profile `survival.models` (§3) |
| Tailscale Serve for :8080 | **on** (pattern from `node.yaml`) | **off** (our listeners removed one by one, both patterns; never `tailscale serve reset`) | **off** |
| Tailscale daemon (`tailscaled`) | untouched | untouched (we don't log out, so going back online is instant) | untouched |
| Cloud link (heartbeat/claim) | **on** if enrolled | **off** | **off** |
| Scheduled ark sync | **on** | **off** (timer stays, run exits `skipped: mode`) | **off** |
| Manual `aictl ark sync` | allowed | allowed with `--allow-offline-mode` confirmation (uses whatever network exists) | refused (`LAI-SYN-010`) |
| kiwix-serve | on if content present | on if content present | **on** (required; `LAI-SVC-001` if no ZIMs) |
| whisper server | on if model present | on if model present | **on** |
| Piper TTS | available | available | available |
| Local UI home page | Dashboard | Dashboard | **Survival Portal** (`/portal`) |
| ai-desktop on launch | Dashboard | Dashboard | Portal full screen |
| Admin PIN required for destructive ops | yes (desktop/UI), CLI requires root | same | same |
| Power profile | `performance` (installer default) | unchanged | unchanged (laptop: also shows battery % prominently) |

## 2. Transitions

```
            aictl mode offline / UI toggle
   ┌────────┐  ───────────────────────────►  ┌─────────┐
   │ online │                                │ offline │
   └────────┘  ◄───────────────────────────  └─────────┘
       │  ▲        aictl mode online              │  ▲
       │  │                                       │  │
       │  └──────────── aictl mode online ────┐   │  │ aictl mode offline
       │                                      │   ▼  │
       └──────── aictl mode survival ──────►  ┌──────────┐
                (any mode → survival)          │ survival │
                                               └──────────┘
```

All transitions are allowed in every direction. Each transition is an **ordered, idempotent, resumable** sequence of steps. The agent records the step in `state.json` (`mode_transition: {target, step, started_at}`), so a crash or reboot mid-transition resumes at the recorded step on agent start.

### 2.1 → `offline`
> The numbered lists below describe *what* each transition does. The implementation step names and their exact order (`link_stop`, `sync_cancel`, `prober_stop`, `tailscale_off`, `persist`, `emit`, …) are defined in [agent.md](../specs/agent.md) §4.2.1, which is authoritative for them. Recording `mode_transition` is done by the engine before every step (agent.md §4.2.2), not as a step of its own.

1. Set `state.mode_transition = {target: offline}`.
2. Stop the cloud link loop. Send a final `deregister`-style heartbeat if reachable, with a 3 s timeout and errors ignored.
3. Cancel any running sync **gracefully**. Partial downloads stay as `.part` and are resumable.
3a. Stop the connectivity prober and set `connectivity: not_checked` (step `prober_stop`).
4. Remove our Tailscale Serve listeners for **both** patterns, regardless of `node.yaml.tailscale.serve_pattern` (`tailscale serve --https=443 off` and `tailscale serve --tcp=18080 off`, each only if our listener exists; agent.md §4.7.3). Never run `tailscale serve reset`, `tailscale down` or `logout`.
5. Persist `mode: offline` and clear `mode_transition`.
6. Emit event `mode.changed`.

### 2.2 → `survival`
1–4. Same as `offline`.
5. Apply the profile's `survival` block (§3). Load the models in order and wait for `ready`, with a timeout of 300 s per model. A failure to load the preferred model falls back down the list (`LAI-LLM-020` warning, not fatal).
6. Ensure `localai-kiwix` and `localai-whisper` are started. A missing content root produces a warning, shown in the Portal as "Library not available, see manual page X".
7. Persist `mode: survival` and clear `mode_transition`.
8. Emit `mode.changed`. ai-desktop and the local UI react by showing the Portal.

Survival entry must **never** fail as a whole. Each sub-step that fails is reported as a warning in the Portal's status strip, and the mode is still entered.

### 2.3 → `online`
1. Re-enable Tailscale Serve per `node.yaml`. If `tailscaled` is not logged in, warn `LAI-NET-003` and continue.
2. Start the cloud link loop (if `/etc/localai/cloud-credential` exists).
3. Leave the models as they are. Do not unload the survival set, because the operator decides.
4. Persist `mode: online` and emit `mode.changed`.
5. If the last successful sync is older than 24 h, schedule a sync to start in 5 minutes. The operator may cancel it.

### 2.4 Permissions
- CLI: `aictl mode …` requires the agent token, which members of `localai-operators` can read. Switching to `survival` **never** requires the PIN; anyone at the keyboard must be able to do it. Switching *out of* survival requires the PIN in the desktop/UI. The CLI accepts it from members of `localai-operators` without a PIN.
- Cloud: an operator can switch a node to `offline`. The node then drops the link, so returning it to `online` must happen locally. The cloud UI warns about this. The cloud cannot command `survival`. Survival is a local decision.

## 3. Profile survival block

Defined per profile in `catalog/profiles/<id>.yaml` ([catalog.md](../specs/catalog.md) §4):

```yaml
survival:
  models:            # ordered; load as many as models_max allows, first that fits wins slot 1
    - qwen3.5-9b-q4km        # general chat / reasoning
    - <vision-model-id>      # vision (photo questions)
  default_model: qwen3.5-9b-q4km
  require_services: [kiwix, whisper]
  portal_language: auto      # auto | en | es  (auto = OS locale)
```

## 4. Connectivity (orthogonal to mode)

The agent continuously derives `connectivity`, which is reported in `/api/v1/status` and shown everywhere:

| Value | Meaning | How detected (every 30 s, all with 3 s timeouts) |
|---|---|---|
| `full` | Internet and the cloud are reachable | `HEAD https://ai.marin.cr/api/health` → 2xx |
| `internet` | Internet is OK but the cloud is down | the cloud check fails, `HEAD https://huggingface.co` → 2xx/3xx |
| `tailnet` | Only the tailnet is up (rare) | `tailscale status --json` shows `BackendState=Running` and peers online, but internet checks fail |
| `none` | Nothing | everything fails |

In `offline`/`survival` modes the agent **does not probe** (privacy and battery), and reports `connectivity: not_checked`.

**Suggestion, never automatic:** if a node is `online` and connectivity has been `none` for more than 30 minutes, the local UI and ai-desktop show a banner: "No internet for 30 minutes. Everything here still works. Switch to Survival mode?" The mode is never changed automatically, because an unexpected change would confuse operators.
