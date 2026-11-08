# Doctor codes registry

> **Status:** approved design · **Code:** `agent/internal/doctor/codes.go` (machine copy) · **CI:** `scripts/check-doctor-codes.sh`
> Canonical names and code areas: [overview.md](../architecture/overview.md) §3.7.

Every problem a user can see — an API error, a doctor result, an alert, a CLI message, a Portal warning — carries a **doctor code** and a one-sentence fix (AGENTS.md rule 4). This file is the **registry** of all codes, one section per area.

> **Authority and sync.** Each owner spec's own §8 table is **authoritative** for its area. This registry is a copy that must be kept identical: any PR that adds, changes or retires a code edits the owner spec's §8 **and** this file **and** `agent/internal/doctor/codes.go`. The CI check `scripts/check-doctor-codes.sh` (§1.6) fails the build when they disagree.

## 1. Rules

### 1.1 Format
- `LAI-<AREA>-<NNN>`: `AREA` is one of the prefixes in §1.3 (2–3 upper-case letters); `NNN` is three digits, `001`–`999`, zero-padded.
- Regex: `^LAI-(AGT|CLI|LLM|CAT|SYN|ARK|SVC|NET|CLD|GW|DSK|INS|KIT|DRL|SEC|HW)-[0-9]{3}$`.
- Codes are printed exactly like that in every language. They are never translated, abbreviated or lower-cased.

### 1.2 Stability
- A code is **permanent**. It is never renumbered and never reused for another meaning, because codes are printed in runbooks, on paper with the ark drive, and in old logs.
- Changing the meaning of a condition means a **new** code. Rewording the message or the fix is allowed.
- A code that is no longer emitted stays in the tables, with severity `retired (vX.Y)` and the fix "No longer used".
- A **family** (several codes for one kind of condition, numbered in code) may be listed as a range, e.g. `LAI-CAT-010..019`; each concrete code in the family still appears in `codes.go` with its own message.

### 1.3 Allocation per area (from overview §3.7)
| Area | Prefix | Owner spec | Reserved blocks inside the area |
|---|---|---|---|
| Agent core / config | `LAI-AGT` | [agent.md](agent.md) §8.1 | `020–029` generic API errors (bad request, not found, busy, mode, starting, too large); `099` internal error |
| CLI | `LAI-CLI` | [aictl.md](aictl.md) §8 | — |
| LLM / router | `LAI-LLM` | [llama-router.md](llama-router.md) §8 | `020–021` survival model fallback (referenced by modes.md) |
| Catalog / profile | `LAI-CAT` | [catalog.md](catalog.md) §7 | `010–019` lint rules |
| Ark sync | `LAI-SYN` | [ark-sync.md](ark-sync.md) | `010` sync refused in survival (referenced by modes.md) |
| Ark drive | `LAI-ARK` | [ark-sync.md](ark-sync.md) | — |
| Companion services | `LAI-SVC` | [companion-services.md](companion-services.md) §8 (`001–099`), [survival-portal.md](survival-portal.md) §8 (`100–149`) | `001` no ZIM library content (referenced by modes.md); `100–149` Survival Portal |
| Network / Tailscale | `LAI-NET` | [agent.md](agent.md) §8.2 | `003` tailscaled not logged in (referenced by modes.md) |
| Cloud link | `LAI-CLD` | [cloud-app.md](cloud-app.md) | **`050–069` agent-side link client, defined in [agent.md](agent.md) §8.5** |
| Gateway | `LAI-GW` | [gateway.md](gateway.md) | — |
| Desktop | `LAI-DSK` | [desktop.md](desktop.md) | — |
| Installer | `LAI-INS` | [installer.md](installer.md) | — |
| Rebuild kit | `LAI-KIT` | [rebuild-kit.md](rebuild-kit.md) | — |
| Drill | `LAI-DRL` | [drill.md](drill.md) | — |
| Security / keys / PIN | `LAI-SEC` | [agent.md](agent.md) §8.4 (security-model.md has no code table) | — |
| Hardware (GPU/disk/thermal/power) | `LAI-HW` | [agent.md](agent.md) §8.3 | — |

Allocation procedure: the owner spec takes the **next free number** in its area (or in a reserved block it owns) in the same PR that first uses it. Another spec that needs a code in a foreign area asks the owner (or adds it to the owner's §8 in the same PR, with the owner's review). Suggested grouping inside an area (not enforced): `001–019` "is it there / is it running", `020–049` operations, `050–099` integrations and rare conditions.

### 1.4 Severity
| Severity | Meaning | Effect |
|---|---|---|
| `info` | Worth knowing; nothing is wrong | Doctor exit 0; not shown in the Portal |
| `warning` | Degraded or at risk; the node still works | Doctor exit 8; yellow in UIs; shown in the Portal status strip if it affects survival features |
| `error` | A feature does not work | Doctor exit 9; red in UIs |
| `critical` | Data, privacy or the whole node is at risk, or nothing can answer | Doctor exit 9; red and bold; always shown in the Portal; raised as a node event to the cloud |

A code has one default severity; a few codes escalate (documented in the "Sev" column, e.g. `warning (critical at lockout)`).

### 1.5 Message and fix text
- **Message:** one sentence in everyday words; what happened, not why the code ran. ≤ 120 characters. (`codes.go` enforces the limit on its titles; two older rows, `LAI-CAT-013` and `LAI-CAT-018`, are longer here, so their title in `codes.go` is a shortened form. A new row stays within the limit.)
- **Fix:** one sentence (two at most), imperative, with the exact command or button in backticks. Never "contact support" alone; say what to report.
- Every code has an English **and** a Spanish version in `codes.go` (`es` is shown in the Portal, the desktop and `aictl --lang es`). This file holds the English text; the Spanish text lives only in code and is reviewed in the PR.
- Codes never contain secrets in their text or `details`.

### 1.6 Keeping things in sync: `scripts/check-doctor-codes.sh`
The script (bash + `grep`/`awk`, no network) runs in CI (`docs-guard`, job `doctor-codes`) and locally. `scripts/tests/test-doctor-codes.sh` proves each rule against fixture trees in `scripts/tests/doctor/`. It fails with `ERROR <file>:<line>: <reason>` when:
1. a code matching `LAI-[A-Z]+-[0-9]{3}` appears in `README.md`, `AGENTS.md`, `CLAUDE.md`, `agent/`, `apps/`, `packages/`, `installers/` or `docs/` but is not defined in this registry (this also rejects placeholders that look like a real code, for example an unknown area with digits);
2. a code row in an owner spec's doctor table is missing from this registry, or a registry code is missing from its owner spec's table (the owner is the spec listed in §1.3; `LAI-CLD-050..069` is owned by agent.md);
3. severity or meaning differ between the owner spec and this registry (compared after trimming and collapsing whitespace; the fix text is not compared);
4. a code appears twice, has an unknown area prefix, sits in a registry section of another area, or is defined in a spec that does not own its area;
5. once `agent/internal/doctor/codes.go` exists: it lacks a code from this registry, has one that is not here, defines a code twice, a registry code has no table entry, an entry's `Severity` differs from the first word of the registry severity, an entry has no non-empty Spanish (`ES:`) title or fix, or a non-test Go file outside `codes.go` hard-codes a `"LAI-…"` literal instead of using the constant (the other packages alias the constants of `codes.go`: `config.CodeNodeYAMLMissing = doctor.AGT001`);
6. a range (`..`) is used outside a documented family. The families are listed in the script (`FAMILIES`): `LAI-CAT-010..019`, `LAI-CLD-050..069`, `LAI-INS-040..044`. Adding a family means editing the script and this section;
7. the action table of §1.7 lists a code twice, or (once `codes.go` exists) a code's `Action:` in `codes.go` differs from the action the table lists for it, or an entry has an `Action:` the table does not list for that code.

The script reads the `codes.go` layout described in its header: a const block (`AGT001 = "LAI-AGT-001"`) and one table entry per code with `ID:`, `Severity:`, `Title:` and `Fix:` on their own lines, each text as `Msg{EN: "…", ES: "…"}`. That the Spanish text is *good* is still a review item; the script and `codes_test.go` only prove it is present, differs from the English, and keeps the commands in backticks. A change to the English fix text needs the matching `codes.go` edit by hand (the script does not compare fixes), as does a new or reworded `es` text. An action is one more line in the entry, `Action: "<action>",` after `Fix:` (§1.7).

### 1.7 Actions: a button for the fix
A non-expert must not need a terminal to apply a fix (AGENTS.md rule 4, [ADR-0009](../decisions/0009-optimized-for-humans.md)). A code whose fix is one of the actions below carries that action: the Survival Portal shows it as the primary button of the problem (survival-portal.md §4.2, §4.1.8) and keeps the typed command of the fix as a small second line. The agent copies the action into every alert and capability that carries the code (`action` in `GET /api/v1/status` and `GET /api/v1/portal/status`, agent.md §3.8.4, survival-portal.md §3.5).

The set is **closed** (v1): adding an action means a new row here, the constant in `agent/internal/doctor/doctor.go` (`Actions()`), the route or screen it uses, and the Portal button with its EN and ES label. A code has at most one action. The fix text keeps its typed command, because the CLI, the logs and the printed runbooks still show it.

| Action | Button (Portal, en) | What it does | Typed command | Codes |
|---|---|---|---|---|
| `ark.import` | Copy from the AI-ARK drive | `POST /api/v1/ark/import` with the safe default: the first connected, usable AI-ARK drive, the profile's items, nothing deleted (survival-portal.md §3.3) | `aictl ark import` | LAI-LLM-008, LAI-CLD-025, LAI-ARK-020, LAI-SVC-001, LAI-SVC-010, LAI-SVC-022, LAI-SVC-030, LAI-SVC-031, LAI-SVC-032, LAI-KIT-040, LAI-DRL-011 |
| `service.restart:kiwix` | Restart the library | `POST /api/v1/services/kiwix/restart` | `aictl services restart kiwix` | LAI-SVC-002, LAI-SVC-005, LAI-DRL-030 |
| `service.restart:whisper` | Restart speech | `POST /api/v1/services/whisper/restart` | `aictl services restart whisper` | LAI-SVC-011, LAI-DRL-032 |
| `portal.brain` | Choose a brain | opens the Portal's Switch brain screen (`/portal/brain`); calls nothing | — | LAI-SVC-102, LAI-SVC-106, LAI-SVC-107, LAI-DRL-090 |

Every action is something a Portal session may already do, so a button never needs the PIN. A code whose fix needs more than these (an item named with `--item`, a PIN, a command with a value to fill in) still gets the generic action when the generic one is safe and does the fix: `LAI-SVC-031` and `LAI-SVC-032` name one item, and `ark.import` imports every item of the profile the drive holds, which includes it. Giving an action to the codes that have none yet is a follow-up, not a rule.

## 2. Registry

### `LAI-AGT` — agent core and config ([agent.md](agent.md) §8.1)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-AGT-001 | error | `/etc/localai/node.yaml` is missing | Restore it from the backup: `sudo cp /etc/localai/node.yaml.bak /etc/localai/node.yaml`, or run the installer again |
| LAI-AGT-002 | error | `node.yaml` cannot be read or has invalid values (warning for unknown keys) | Fix the line named in the details, or restore `node.yaml.bak`. The last good settings stay in use until then |
| LAI-AGT-003 | warning (critical when defaults and offline mode were used) | The state file was damaged; the agent used the backup or started in offline mode | Check the mode with `aictl status` and set it again with `aictl mode <mode>` |
| LAI-AGT-004 | critical | The agent cannot write its state (`/var/lib/localai` full or read-only) | Free disk space (`aictl ark status` shows what uses it) and restart the agent: `sudo systemctl restart localai-agent` |
| LAI-AGT-005 | error | The agent socket `/run/localai/agent.sock` could not be created | Restart the agent: `sudo systemctl restart localai-agent` |
| LAI-AGT-006 | error | Port 7070 is used by another program, so the local UI is unavailable | Find it with `sudo ss -ltnp 'sport = :7070'`, stop it, then restart the agent |
| LAI-AGT-007 | info | A mode change was interrupted and has been resumed | Nothing to do |
| LAI-AGT-008 | warning | One step of a mode change failed or was skipped | Open `aictl doctor` to see which part failed. The mode itself is active |
| LAI-AGT-009 | error | An agent update failed and the previous version was restored | Nothing is broken. Report the version in the details to the maintainer |
| LAI-AGT-010 | critical | A downloaded update failed its signature or checksum check and was discarded | Do not install it by hand. Report it to the maintainer |
| LAI-AGT-011 | critical | The agent is not running as root | Start it only through systemd: `sudo systemctl start localai-agent` |
| LAI-AGT-012 | warning | The computer's clock looks wrong | Set the correct date and time in system settings. Offline features still work |
| LAI-AGT-013 | warning | The audit log cannot be written | Free disk space in `/var/lib/localai` |
| LAI-AGT-014 | warning | The agent restarted more than 3 times in the last hour | Run `aictl logs --unit agent` and report the last error |
| LAI-AGT-015 | info | An agent update is available (or: already up to date) | Run `sudo aictl update` when convenient |
| LAI-AGT-016 | info | A settings change needs a restart to take effect | Run `sudo systemctl restart localai-agent` (or `aictl restart llama` when the details say so) |
| LAI-AGT-017 | warning | A cloud command was interrupted because the agent restarted | Run the command again from ai.marin.cr if it is still needed |
| LAI-AGT-020 | error | The request was not valid | Check the command or the value named in the details |
| LAI-AGT-021 | error | The thing asked for does not exist (model, service, content, route) | Check the name with `aictl models` or `aictl status` |
| LAI-AGT-022 | warning | Another operation is already running | Wait for it to finish (`aictl status`), then try again |
| LAI-AGT-023 | warning | This action is not allowed in the current mode | Switch mode first (`aictl mode online`), or add the option named in the details |
| LAI-AGT-024 | info | The agent is starting or stopping | Wait a few seconds and try again |
| LAI-AGT-025 | error | The request or upload is too large | Use a smaller file |
| LAI-AGT-099 | error | Internal agent error | Run `aictl logs --unit agent` and report the request id in the details |

### `LAI-CLI` — command-line interface ([aictl.md](aictl.md) §8)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CLI-001 | error | `aictl` cannot reach the agent | Start it: `sudo systemctl start localai-agent`. If it keeps stopping: `journalctl -u localai-agent -n 50` |
| LAI-CLI-002 | error | Your user may not talk to the agent | Run `sudo usermod -aG localai-operators $USER`, then log out and back in |
| LAI-CLI-003 | warning | `aictl` and the agent are different versions | Usually temporary during an update. If it stays, run `sudo systemctl restart localai-agent` |
| LAI-CLI-004 | error | The command took too long | Check `aictl status`; the action may still finish in the background |
| LAI-CLI-005 | error | Unknown command or option | Run `aictl help` |
| LAI-CLI-006 | error | This command must be run as root | Run it with `sudo aictl …` |
| LAI-CLI-007 | error | This command needs a yes/no confirmation | Run it in a terminal, or add `--yes` |
| LAI-CLI-008 | error | The admin PIN is needed but there is no terminal to ask for it | Run it in a terminal, use `sudo`, or pass `--pin-stdin` |
| LAI-CLI-009 | error | The agent sent an answer `aictl` does not understand | Update with `sudo aictl update`, or report the request id |
| LAI-CLI-010 | info | You stopped waiting; the action continues in the background | Check with `aictl status` |
| LAI-CLI-011 | warning | No clipboard tool is available | Use `aictl key show` and copy the key by hand |
| LAI-CLI-012 | error | A file could not be read or written | Check the path and free space named in the message |
| LAI-CLI-013 | warning | `inventory.local.yaml` was not found; placeholders were left in the documents | Plug in the ark drive or pass `--inventory <file>` |
| LAI-CLI-014 | info | No PDF printer (Chromium) is installed; only HTML was made | Install `chromium` to also get PDFs, or print the HTML pages from a browser |
| LAI-CLI-015 | error | The survival card does not fit on one page | Shorten the text in `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12) |
| LAI-CLI-016 | error | The survival card would show a secret value (an address, the tailnet or an e-mail) | Remove that placeholder from `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12) |

### `LAI-LLM` — llama-server router ([llama-router.md](llama-router.md) §8)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-LLM-001 | critical | The AI model service (`localai-llama`) is not running | Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama` |
| LAI-LLM-002 | critical | The llama-server program is missing | Run `sudo aictl rebuild-llama` |
| LAI-LLM-003 | error | Another program is using port 8080 | Run `sudo ss -ltnp 'sport = :8080'` to see it; if it is `llama-qwen`, run `sudo aiagent migrate llama-qwen` |
| LAI-LLM-004 | error | The AI model service is running but not answering | Run `aictl restart llama` |
| LAI-LLM-005 | critical | The AI model service rejects this node's own API key | Run `aictl restart llama`; if it persists, `sudo aictl key rotate` |
| LAI-LLM-006 | warning | Someone edited `/etc/localai/models.ini` by hand; it was regenerated (copy saved next to it) | Make changes in the catalog or `node.yaml` instead |
| LAI-LLM-007 | error | The model list for the AI service could not be generated | Run `aictl doctor` and report the details |
| LAI-LLM-008 | warning | Model files are missing or not verified | Run `aictl download <model>` (online) or `aictl ark import` (from the ark drive) |
| LAI-LLM-009 | error | A model failed to start (or stopped unexpectedly); the previous model was started again | Choose a smaller model (`aictl models --loadable`) or check `aictl logs --unit llama` |
| LAI-LLM-010 | error | A model took too long to start; the previous model was started again | Try again; models on the ark drive load slower. Copy it inside with `aictl ark import` |
| LAI-LLM-011 | warning | Not enough room to load this model without unloading others | Unload a model first, or allow unloading others |
| LAI-LLM-012 | critical | No model could be started, not even the previous one | Run `aictl restart llama`, then `aictl load <default model>`. See the troubleshooting runbook |
| LAI-LLM-013 | error | The default model did not start at boot | Run `aictl load <model>` or choose another default with `aictl default <model>` |
| LAI-LLM-014 | warning | No default model is set or available | Run `aictl default <model>` |
| LAI-LLM-015 | warning | Two models need different settings for the same option; it was left out | Report it to the catalog maintainer, or set it in `node.yaml` `llama.extra_env` |
| LAI-LLM-016 | warning | The old `llama-qwen.service` is still enabled or running | Run `sudo aiagent migrate llama-qwen` |
| LAI-LLM-017 | error | The migration was stopped before changing anything (model not in catalog, or settings differ) | Read the details; add the model to the catalog, or re-run with `--accept-catalog-flags` |
| LAI-LLM-018 | error | Building llama.cpp failed | Check the build log in the details; install the missing package named there |
| LAI-LLM-019 | error | The new llama.cpp build did not work; the previous build is still in use | Report the build log; nothing else to do |
| LAI-LLM-020 | warning | In survival mode the preferred model did not start; the next one is used | Nothing to do now. Later, check `aictl logs --unit llama` |
| LAI-LLM-021 | critical | In survival mode no model could be started | Run `aictl restart llama`. Library, maps and manuals still work |
| LAI-LLM-022 | warning | A model is running on the CPU instead of the GPU (very slow) | Restart the computer; check the GPU driver (`aictl doctor` shows which) |
| LAI-LLM-023 | info | The AI service runs in single-model mode | Nothing to do. To switch back: `llama.mode: router` in `node.yaml` |
| LAI-LLM-024 | warning | Extra models from a download cache are visible to the AI service | Empty `/var/lib/localai/llama-cache` and run `aictl restart llama` |
| LAI-LLM-025 | error | The benchmark failed | Make sure no other GPU program runs and try again |
| LAI-LLM-026 | warning | The model list served on port 8080 does not match the catalog | Run `aictl restart llama`; if it persists, report it |
| LAI-LLM-027 | error | A model was stopped because the computer ran out of memory | Choose a smaller model or a shorter context |
| LAI-LLM-028 | warning | More models are loaded than this node allows | Run `aictl unload <model>` for the ones you don't need |
| LAI-LLM-029 | error | The migration from the old service was interrupted | Run `sudo aiagent migrate llama-qwen` again (it continues), or `--rollback` |
| LAI-LLM-030 | warning | A model was running from the ark drive and the drive was removed | Plug the drive back in, or load another model |
| LAI-LLM-031 | warning | A model would not stop, so the AI service was restarted | Nothing to do |

### `LAI-CAT` — catalog and profiles ([catalog.md](catalog.md) §7)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CAT-001 | error | Catalog file can't be parsed | The catalog file is damaged. Run `aictl catalog restore`, which copies the last good one from the ark kit |
| LAI-CAT-002 | warning | New catalog failed validation, previous kept | Nothing to do. Report it to the maintainer |
| LAI-CAT-003 | error | The profile in node.yaml doesn't exist | Run `aictl profile suggest`, then `aictl profile set <id>` |
| LAI-CAT-004 | error | The profile references an unknown model/content id | Catalog bug. Fix it in the repo |
| LAI-CAT-005 | warning | A model doesn't fit this node (VRAM/RAM/backend) | Choose a smaller model. `aictl models --loadable` lists the ones that fit |
| LAI-CAT-006 | warning | Couldn't download the catalog from GitHub | The node keeps its current catalog. Check the internet connection, then run `aictl catalog refresh` |
| LAI-CAT-007 | warning | The catalog version named in node.yaml does not exist on GitHub | Set `catalog.channel` in node.yaml to `stable` or to an existing `catalog-<version>` |
| LAI-CAT-008 | warning | The catalog from GitHub was incomplete or inconsistent, so nothing was installed | The node keeps its current catalog. Run `aictl catalog refresh` again; if it repeats, report it to the maintainer |
| LAI-CAT-010 | error | Lint: schema problem (unknown key, missing required field, wrong type, `schema_version` not 1) | Fix the YAML at the reported path |
| LAI-CAT-011 | error | Lint: pinning problem (`source.revision` not 40-hex, `sha256` not 64 hex, `size_bytes` missing) | Fix the YAML at the reported path |
| LAI-CAT-012 | error | Lint: bad id format or duplicate id across models and ark | Fix the YAML at the reported path |
| LAI-CAT-013 | error | Lint: model file rules (llama block missing, vision without mmproj, draft, chat_template file, piper needs `piper_onnx` + `piper_config`, whisper needs one `whisper` file) | Fix the YAML at the reported path |
| LAI-CAT-014 | error | Lint: forbidden `extra_args` or invalid `llama.env` key | Fix the YAML at the reported path |
| LAI-CAT-015 | error | Lint: ark item rules (kiwix `refresh_days`, bbox, kit/docs tier 1, kit id ↔ codename/arch) | Fix the YAML at the reported path |
| LAI-CAT-016 | error | Lint: optional field misused (`extract`, `extra_repos`/unknown key name, `checksum`/`trust`/`tofu`, `url` vs `files`) | Fix the YAML at the reported path |
| LAI-CAT-017 | error | Lint: `portal_links` rule broken | Fix the YAML at the reported path |
| LAI-CAT-018 | error | Lint: profile rule broken (`router.models_max` ≥ 1, `router.autoload` false, `default_model`/`survival.*` inside `include`, `ark-drive-full` has no `match`, overlapping `match` ranges) | Fix the YAML at the reported path |
| LAI-CAT-019 | error | Lint: `CATALOG_VERSION` missing or not `YYYY.MM.DD-N`, or wrong file layout | Fix the YAML at the reported path |

### `LAI-NET` — network and Tailscale ([agent.md](agent.md) §8.2)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-NET-001 | warning | Tailscale is not installed (online mode cannot publish this node) | Install Tailscale ([runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md)) or set `tailscale.serve_pattern: none` |
| LAI-NET-002 | warning | The Tailscale service is not running | Run `sudo systemctl start tailscaled` |
| LAI-NET-003 | warning | Tailscale is not logged in | Run `sudo tailscale up` and sign in. Local features keep working |
| LAI-NET-004 | error | Tailscale Serve could not be changed for port 8080 | Run `tailscale serve status` and the command in the details by hand, then `aictl doctor` |
| LAI-NET-005 | warning | Another Tailscale Serve listener uses our port, or an extra listener for port 8080 exists | Remove the extra listener with the exact command in the details (e.g. `sudo tailscale serve --tcp=18080 off`) |
| LAI-NET-006 | critical | This node is still published on the tailnet while in offline or survival mode | Run `sudo tailscale serve --https=443 off` and `sudo tailscale serve --tcp=18080 off` |
| LAI-NET-007 | info | The Tailscale Serve listener was missing and has been restored | Nothing to do |
| LAI-NET-008 | info | The tailnet HTTPS certificate is still being issued | Wait one minute and try again |
| LAI-NET-009 | critical | Tailscale Funnel (public internet exposure) was enabled for this node's model port; it has been turned off | Never enable Funnel on AI nodes. Check who changed it |
| LAI-NET-010 | critical | A Local AI service is listening on a network address, not only on this computer | Run `aictl doctor` for the service name and restart it: `aictl restart <service>` |
| LAI-NET-011 | info | No internet for 30 minutes | Everything here still works. Switch to Survival mode if this is an emergency: `aictl mode survival` |
| LAI-NET-012 | info | The internet works but the cloud (ai.marin.cr) does not answer | Nothing to do on this node. Local features work |
| LAI-NET-013 | warning | Names cannot be looked up (DNS), so downloads and the cloud fail | Check the network connection or router |
| LAI-NET-014 | warning | Wi-Fi power saving is on, which makes a laptop server drop off the network | Run the laptop setup again: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md) |

### `LAI-HW` — hardware ([agent.md](agent.md) §8.3)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-HW-001 | error | No usable GPU was found, but this node is set up for one | Check that the graphics driver is installed (`nvidia-smi` or `vulkaninfo --summary`). The CPU-only models still work |
| LAI-HW-002 | error | The NVIDIA driver is not answering (`nvidia-smi` failed) | Restart the computer. If it persists, reinstall the driver ([runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md)) |
| LAI-HW-003 | error | The AMD GPU device files are missing (`/dev/kfd`, `/dev/dri/renderD*`) | Restart the computer; check that the `amdgpu` driver loads |
| LAI-HW-004 | error | The `localai` user cannot use the GPU (not in `video`/`render`) | Run `sudo usermod -aG video,render localai` and `aictl restart llama` |
| LAI-HW-005 | warning | Other programs are using a lot of graphics memory | Close games or other GPU programs, or log out of the desktop session |
| LAI-HW-006 | warning | The computer is almost out of memory (RAM) | Close other programs or load a smaller model |
| LAI-HW-007 | warning | Free disk space is below the profile's safety limit | Remove files you don't need, or move content to the ark drive |
| LAI-HW-008 | critical | A disk used by Local AI is more than 95% full | Free space now: delete large personal files or old models (`aictl delete <model>`) |
| LAI-HW-009 | warning | The GPU is very hot (87 °C or more) | Make sure the vents are not blocked and the laptop is on a hard surface |
| LAI-HW-010 | warning | The CPU is very hot (95 °C or more) | Same as above; clean the fans if dusty |
| LAI-HW-011 | warning | The computer is slowing down to cool itself (throttling) | Improve airflow; answers will be slower until it cools |
| LAI-HW-012 | warning | Running on battery | Plug in the charger. A laptop server stops when the battery runs out |
| LAI-HW-013 | warning | Battery below 20% | Plug in the charger soon |
| LAI-HW-014 | critical | Battery below 10% | Plug in the charger now, or save your work |
| LAI-HW-015 | info | The power profile is not "performance" | Run `powerprofilesctl set performance` |
| LAI-HW-016 | warning | The computer is using a lot of swap, which makes everything slow | Load a smaller model or close other programs |
| LAI-HW-017 | error | The configured backend does not match the hardware (e.g. CUDA set, no NVIDIA GPU) | Run `aictl profile suggest` and fix `llama.backend` in `node.yaml` |
| LAI-HW-018 | error | Vulkan only sees a software renderer (llvmpipe), so models run on the CPU | Install the Mesa Vulkan drivers (`mesa-vulkan-drivers`) and restart |
| LAI-HW-019 | error | The GPU's CUDA architecture could not be detected or is not supported by this build | Set `llama.cuda_arch` in `node.yaml` and run `sudo aictl rebuild-llama` |
| LAI-HW-020 | warning | The laptop will sleep when the lid is closed | Run the laptop setup again: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md) |

### `LAI-SEC` — security, keys, PIN ([agent.md](agent.md) §8.4)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-SEC-001 | error | Not signed in: the agent token or session is missing or wrong | Use `aictl` as a member of the `localai-operators` group, or sign in with a code from `aictl ui-code` |
| LAI-SEC-002 | warning | This action needs the admin PIN | Enter the admin PIN when asked, or run the command with `sudo` |
| LAI-SEC-003 | warning | Wrong PIN | Try again. After 5 wrong tries you must wait 5 minutes |
| LAI-SEC-004 | warning (critical at lockout) | Too many wrong PIN or login-code attempts | Wait the time shown. Forgotten PIN: `sudo aictl pin set` |
| LAI-SEC-005 | error | No admin PIN has been set | Run `sudo aictl pin set` |
| LAI-SEC-006 | error | Request refused: wrong address (Host header) | Open `http://127.0.0.1:7070` exactly |
| LAI-SEC-007 | error | Request refused: it came from another website | Open the page from `http://127.0.0.1:7070` or the ai-desktop app |
| LAI-SEC-008 | warning | The login code is wrong, used, or expired | Get a new code with `aictl ui-code` |
| LAI-SEC-009 | warning | A secret file had unsafe permissions; they were fixed | Nothing to do. If it happens again, check who changed `/etc/localai` |
| LAI-SEC-010 | critical | The llama API key file is missing or not 64 hex characters | Run `sudo aictl key rotate` |
| LAI-SEC-011 | warning | The agent token was missing and a new one was created | Restart ai-desktop; scripts must re-read `/etc/localai/agent-token` |
| LAI-SEC-012 | info | Your session expired | Sign in again |
| LAI-SEC-013 | error | Your user is not allowed to use the agent | Ask an admin to run `sudo usermod -aG localai-operators <user>`, then log out and in |
| LAI-SEC-014 | critical | llama-server answers without the API key | Run `aictl restart llama`; if it persists, run `aictl doctor` and report it |
| LAI-SEC-015 | warning | The old key path `/etc/llama-qwen/api-key` is not a link to the current key | Run `sudo aiagent migrate llama-qwen` again (it only fixes the link) |

### `LAI-CLD` — cloud link (owner: [cloud-app.md](cloud-app.md))
> Codes `001–049`, copied from [cloud-app.md](cloud-app.md) §8 (authoritative). `070–999` are unallocated.

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CLD-004 | error | The credential belongs to a different node id | Check `node_id` in `/etc/localai/node.yaml`, or enroll with the credential made for this node |
| LAI-CLD-008 | warning | The cloud refused this computer's status report | Update the agent (`sudo aictl update`) or tell the admin; local use is not affected |
| LAI-CLD-012 | error | A command failed while running | Read the code in the command's error for the exact fix, then send it again |
| LAI-CLD-013 | warning | Couldn't download the catalog from the cloud | The node keeps its current catalog. It retries at the next sync |
| LAI-CLD-015 | error | An agent update failed and the old version was restored | Nothing is broken. Tell the admin; the update will be retried later |
| LAI-CLD-020 | warning | Node stopped reporting | Check the computer is on and connected. If it was moved to Offline mode on purpose, ignore this |
| LAI-CLD-021 | critical | Node offline for more than a day | Turn the computer on or check its network. If it is retired, retire it in the cloud |
| LAI-CLD-022 | warning (critical after 90 days) | The AI-ARK drive hasn't been synced for more than 30 days | Plug the AI-ARK drive into the node and press "Sync now" (or run `aictl ark sync`) |
| LAI-CLD-023 | warning | The last ark sync failed | Open the node's Ark tab, read the sync error code and follow its fix, then press "Sync now" |
| LAI-CLD-024 | critical (warning for a drill warn) | The monthly drill failed | Open the node in the cloud or run `aictl drill` on it and follow the failed check's fix |
| LAI-CLD-025 | critical | Essential (tier 1) ark content is missing | Press "Sync now" while online, or import from the AI-ARK drive (`aictl ark import`) |
| LAI-CLD-026 | warning | The cloud has no API key for this node, so the gateway can't use it | Press "Refresh status" on the node; if it persists run `sudo aictl cloud resend-key` on the node |
| LAI-CLD-027 | warning | The node's public address (ai-nN.marin.cr) is not answering | Check `tailscale serve status` on the node and the vhost on the VPS (runbook 05) |
| LAI-CLD-028 | info | The node uses an old catalog | Press "Refresh catalog" on the node |
| LAI-CLD-029 | info | A newer agent is available | Admin: Lab → node → Update agent |
| LAI-CLD-030 | warning | Commands are waiting longer than they should | Admin: check that the cloud's background jobs run (Admin → Jobs) |
| LAI-CLD-031 | warning | A node was added but never connected | Finish the install on that computer, or retire the node in the cloud |
| LAI-CLD-032 | critical | The cloud can't read its stored keys (encryption key missing or wrong) | Admin: restore `SECRETS_ENCRYPTION_KEY` from the backup, restart, then "Ask all nodes to re-send keys" |
| LAI-CLD-033 | error | The catalog checker (`aictl`) is missing on the cloud server | Admin: redeploy the cloud app (`laictl deploy update`) |
| LAI-CLD-034 | info | The node is in Offline or Survival mode, so it won't take cloud commands | Someone at the computer must switch it back to Online |
| LAI-CLD-035 | error | The node is retired or not enrolled yet | Finish enrolling it, or add a new node |

#### `LAI-CLD-050..069` — agent-side link client ([agent.md](agent.md) §8.5)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CLD-050 | info | This node is not connected to the cloud | To connect it: `sudo aictl cloud enroll --url https://ai.marin.cr` |
| LAI-CLD-051 | error | The cloud rejected this node's credential (revoked or wrong) | Create a new node credential at ai.marin.cr and run `sudo aictl cloud enroll` again |
| LAI-CLD-052 | warning | An operator disabled this node in the cloud | Re-enable it at ai.marin.cr (Nodes page). Local features work |
| LAI-CLD-053 | warning | The cloud cannot be reached right now | Nothing to do; the node retries by itself. Local features work |
| LAI-CLD-054 | error | The cloud refused this node's registration | Check `aictl logs --unit agent` for the details and report them |
| LAI-CLD-055 | info | The cloud is older than this agent; some details are not sent | Update the cloud app |
| LAI-CLD-056 | info | The cloud has no node-state endpoint yet; a reduced status is sent | Update the cloud app |
| LAI-CLD-057 | warning | The cloud asked for an action this node does not allow | Nothing to do. To allow it, edit `cloud.allow` in `node.yaml` |
| LAI-CLD-058 | warning | The cloud sent an invalid command | Report it to the maintainer |
| LAI-CLD-059 | info | A cloud command finished after the cloud stopped waiting for it | Nothing to do; the cloud shows the current state |
| LAI-CLD-060 | warning | The cloud does not have this node's current API key yet | Check the connection; the agent retries every 5 minutes |
| LAI-CLD-061 | warning | Another computer is using this node id | Give each computer its own id in `node.yaml` (`node.id`) |
| LAI-CLD-062 | error | The cloud address must start with https:// | Fix `cloud.url` in `node.yaml` |
| LAI-CLD-063 | info | A cloud command was stopped because the node is going offline | Nothing to do. Run the command again from ai.marin.cr after the node is back online |

### `LAI-SYN` — owner: [ark-sync.md](ark-sync.md)
> Copied from [ark-sync.md](ark-sync.md) §8 (authoritative; Spanish fixes are there). `LAI-SYN-010` = manual sync refused in survival mode ([modes.md](../architecture/modes.md) §1).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-SYN-001 | info | Scheduled sync skipped: no internet | Nothing to do. Sync will try again tonight. |
| LAI-SYN-002 | info | Sync skipped or stopped because the node is in offline or survival mode | Nothing to do. Switch to online mode to sync again. |
| LAI-SYN-003 | info | Scheduled sync skipped: running on battery | Plug in the charger. Sync runs at night when plugged in. |
| LAI-SYN-004 | warning | No successful sync for more than 14 days while online | Check the internet connection, then run `aictl ark sync`. |
| LAI-SYN-005 | warning | Another sync is already running | Wait for it to finish, or cancel it with `aictl ark sync --cancel`. |
| LAI-SYN-010 | warning | Manual sync refused in survival mode | Leave survival mode first (needs the PIN). |
| LAI-SYN-011 | warning | Manual sync in offline mode needs confirmation | Run again with `--allow-offline-mode`, or switch to online mode. |
| LAI-SYN-020 | warning | A download didn't match its checksum and was quarantined | Nothing to do: it will be downloaded again. If it repeats, report it. |
| LAI-SYN-021 | warning | The source's checksum or size disagrees with the catalog | The catalog or the source is wrong. Report it to the maintainer. The old copy is kept. |
| LAI-SYN-022 | warning | Kiwix checksum file missing or unreadable | Nothing to do. It will be retried next run; the old copy is kept. |
| LAI-SYN-023 | warning | Map extract failed `pmtiles verify` | Nothing to do: it will be rebuilt next run. |
| LAI-SYN-024 | warning | A source mirror is damaged or missing the pinned commit | Run `aictl ark sync --item <id>`. If it repeats, delete the mirror with `aictl ark prune`. |
| LAI-SYN-025 | error | A stored file no longer matches its checksum (disk damage), or wasn't re-checked in time | Run `aictl ark verify --full`. If files keep failing, the disk may be dying: copy the ark to another drive. |
| LAI-SYN-026 | error | A swap couldn't complete | Run `aictl ark verify`. The previous version was restored if it existed. |
| LAI-SYN-027 | warning | Downloaded size differs from the expected size | Nothing to do: it will be downloaded again. |
| LAI-SYN-028 | warning | ZIM internal checksum failed | Nothing to do: it will be downloaded again. |
| LAI-SYN-030 | warning | The server refused or no longer has the file (403/404/410) | Update the catalog (`aictl catalog refresh`). If it persists, report it. |
| LAI-SYN-031 | warning | Download failed after all retries | Check the internet connection. It resumes where it stopped next run. |
| LAI-SYN-032 | info | Server doesn't support resume; the file restarted from zero | Nothing to do. |
| LAI-SYN-033 | warning | Couldn't find the newest version at the source | Nothing to do; it retries next run. If it persists, report it. |
| LAI-SYN-034 | warning | Hugging Face needs a login or is rate-limiting | Put a Hugging Face token in `/etc/localai/hf-token`, or wait an hour. |
| LAI-SYN-035 | error | The same item failed in 3 runs in a row | Run `aictl doctor` and read the item's last error. |
| LAI-SYN-040 | warning | Not enough free disk space | Free space on the disk, or lower the profile budget. Old versions in `.trash` are removed automatically when safe. |
| LAI-SYN-041 | warning (error when a survival model is skipped) | Items skipped because the budget is full (a survival model, if named) | Raise `budget_bytes` or remove other content in `node.yaml`. |
| LAI-SYN-042 | error | The rebuild kit or manuals don't fit | Free disk space. Without the kit this computer can't be rebuilt offline. |
| LAI-SYN-043 | info | Old versions were deleted to make room | Nothing to do. |
| LAI-SYN-044 | warning | The last verified copy was kept; nothing was deleted | This is the only verified copy on this computer. Copy it to another drive first (`aictl ark sync --root <drive>`), or delete it anyway with `aictl delete <model> --allow-last-copy`. |
| LAI-SYN-050 | info | Sync journal was damaged and rebuilt | Nothing to do. |
| LAI-SYN-051 | info | Content no longer in the profile is using disk space | Run `aictl ark prune --orphans` if you don't need it. |
| LAI-SYN-052 | warning | An item's MANIFEST is missing or damaged | Run `aictl ark verify`; the item will be fetched again. |
| LAI-SYN-060 | warning | Couldn't compute the package list for the rebuild kit | Report it: a package name in the catalog probably changed. The old package set is kept. |
| LAI-SYN-061 | error | A tool sync needs is missing (pmtiles, git, apt-get) | Run `sudo install-node.sh --repair`. |
| LAI-SYN-062 | warning | Manuals were made without the real lab values | Copy `inventory.local.yaml` to `/etc/localai/` or plug in the ark drive, then sync. |
| LAI-SYN-063 | warning | The rebuild kit has packages from vendor repositories whose signature was not checked | Pin the repository's key and set sync.apt_unverified_repos to refuse. |
| LAI-SYN-070 | error | A drive copy no longer matches its own checksum | That copy is damaged. Sync the drive on a computer with internet, or use another drive. |

### `LAI-ARK` — owner: [ark-sync.md](ark-sync.md)
> Copied from [ark-sync.md](ark-sync.md) §8 (authoritative; Spanish fixes are there).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-ARK-001 | error | No writable content root (no drive and internal read-only) | Plug in an AI-ARK drive, or run `aictl doctor`. |
| LAI-ARK-002 | error | A drive labelled AI-ARK is not ext4 | Reformat it as ext4 (see "Formatting a new AI-ARK drive"). This erases it. |
| LAI-ARK-003 | warning | The ark drive is read-only | It can be read but not updated. Unplug it safely and plug it in again. |
| LAI-ARK-004 | error | The ark drive's filesystem has errors | Eject it, then run `sudo fsck.ext4 -f /dev/<device>` and plug it in again. |
| LAI-ARK-005 | warning | An AI-ARK drive is plugged in but not mounted | Press **Mount** in the app, or open it in the file manager. |
| LAI-ARK-006 | error | Two drives have the same ID (a cloned drive) | Unplug one. Give it a new ID with `sudo tune2fs -U random /dev/<device>`. |
| LAI-ARK-007 | warning | The ark drive was unplugged without ejecting | Next time press **Eject** first. Plug it in again to let sync check it. |
| LAI-ARK-008 | warning (error after 90 days) | The ark drive hasn't been updated for more than 30 days (red after 90) | Plug it into a computer with internet overnight. |
| LAI-ARK-009 | warning | The drive can't be ejected: files are in use | Close the listed programs or switch brain, then eject again. |
| LAI-ARK-010 | info | A new AI-ARK drive needs to be set up | Press **Set up this drive**, or run `aictl ark init`. |
| LAI-ARK-011 | info | The drive is too small for everything; lower tiers were skipped | Nothing to do, or use a bigger drive. |
| LAI-ARK-012 | warning | The lab inventory on the drive and on this computer both changed | Compare the two files and keep the right one. The other is saved as `.conflict`. |
| LAI-ARK-014 | error | The drive reported read/write errors | Eject it and try another USB port or cable. If it repeats, replace the drive. |
| LAI-ARK-015 | warning | Several ark drives are connected; choose one | Run the import again with `--from <root-id>` (see `aictl ark drives`). |
| LAI-ARK-020 | warning | The internal content folder was missing and was recreated empty | Run `aictl ark sync`, or `aictl ark import` from a drive. |
| LAI-ARK-021 | warning | The content folder is on a network disk; it is used read-only | Move `/srv/localai` to a local disk. |

### `LAI-SVC` — owner: [companion-services.md](companion-services.md)
> `001–099` copied from [companion-services.md](companion-services.md) §8; `100–149` copied from [survival-portal.md](survival-portal.md) §8 (both authoritative; Spanish fixes are there). `LAI-SVC-001` = survival mode needs the library but no ZIM content is present ([modes.md](../architecture/modes.md) §1).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-SVC-001 | error | The library has no books | Plug in the ark drive, or run `aictl ark import`. |
| LAI-SVC-002 | error | The library service isn't running | Press **Restart library**, or run `aictl services restart kiwix`. |
| LAI-SVC-003 | error | The library program isn't installed | Run `sudo install-node.sh --repair`. |
| LAI-SVC-004 | warning | One library book couldn't be opened | Run `aictl ark verify`. The other books still work. |
| LAI-SVC-005 | warning | The library list couldn't be rebuilt; the old list is used | Run `aictl services restart kiwix`. If it repeats, run `aictl doctor`. |
| LAI-SVC-010 | error | No speech-recognition model is installed | Run `aictl ark sync --item whisper-small` (online) or `aictl ark import` from the drive. |
| LAI-SVC-011 | error | The speech-recognition service isn't running | Run `aictl services restart whisper`. |
| LAI-SVC-012 | error | The speech-recognition program isn't built for this computer | Run `sudo aictl rebuild-whisper`. |
| LAI-SVC-013 | info | Speech recognition runs on the processor to leave graphics memory for the AI | Nothing to do. |
| LAI-SVC-014 | warning | The recording couldn't be used (wrong format, too long, or silent) | Try again, speak closer to the microphone, and keep it under 2 minutes. |
| LAI-SVC-015 | warning | Too many voice requests at once | Wait a few seconds and try again. |
| LAI-SVC-016 | warning | Speech recognition took too long | Try a shorter recording. |
| LAI-SVC-020 | error | The voice program isn't installed | Run `sudo install-node.sh --repair`. |
| LAI-SVC-021 | warning | No voice for this language; another voice was used | Install a voice for the language with `aictl ark sync`. |
| LAI-SVC-022 | warning | No voice is installed | Answers are shown as text. Install a voice with `aictl ark import` or `aictl ark sync`. |
| LAI-SVC-023 | error | The voice program failed | Try again. If it repeats, run `aictl doctor`. |
| LAI-SVC-024 | info | The text was too long to read aloud at once | Nothing to do: the app reads it in parts. |
| LAI-SVC-030 | warning | No offline map is installed | Plug in the ark drive and run `aictl ark import`. |
| LAI-SVC-031 | warning | Map labels (fonts) are missing | Import `map-assets` from the drive: `aictl ark import --item map-assets`. |
| LAI-SVC-032 | warning | Place search isn't installed | Import `places-central-america`: `aictl ark import --item places-central-america`. |
| LAI-SVC-033 | info | The GPS isn't sending a position | Move near a window or outside, or set your position by hand on the map. |
| LAI-SVC-034 | error | A map file is damaged | Run `aictl ark verify --item <map id>`. |
| LAI-SVC-040 | warning | No model that understands photos can run here | Install a vision model that fits (`aictl models --loadable`). |
| LAI-SVC-041 | error | The vision model is missing its image part (mmproj) | Run `aictl ark verify --item <model id>`; it will be fetched again. |
| LAI-SVC-050 | info | A service was restarted automatically | Nothing to do. |
| LAI-SVC-051 | error | A service keeps failing and was left stopped | Run `aictl doctor` and `aictl logs --unit <unit>`. |
| LAI-SVC-100 | error | The Portal's screens are missing from the program | Use the links on this page. Then run `sudo install-node.sh --repair`. |
| LAI-SVC-101 | info | Chat is locked outside survival mode | Press **Start survival mode**, or log in. |
| LAI-SVC-102 | error | The AI (llama) isn't answering | Press **Switch brain** and choose one, or restart the computer. |
| LAI-SVC-103 | warning | The microphone is blocked or missing | Allow the microphone in the browser (lock icon next to the address), or plug one in. |
| LAI-SVC-104 | warning | This browser can't record | Use the Local AI app or Chromium/Firefox. |
| LAI-SVC-105 | warning | The recording couldn't be processed | Try again. |
| LAI-SVC-106 | warning | No brain is running | Press **Switch brain** and choose one. |
| LAI-SVC-107 | error | The new brain couldn't start | Choose a smaller brain. The previous one was restarted. |
| LAI-SVC-108 | warning | The photo can't be used | Try another photo (JPEG or PNG, under 30 MB). |
| LAI-SVC-109 | warning | A first-aid link points to a missing article | Report it. Use Library search meanwhile. |
| LAI-SVC-110 | info | The Portal session ended | Reload the page. |
| LAI-SVC-111 | warning | Battery low | Plug in the charger when you can. |
| LAI-SVC-112 | critical | Battery very low | Plug in now, or the computer will turn off. |
| LAI-SVC-113 | info | No graphics card in use; answers are slow | Nothing to do. Short questions work best. |
| LAI-SVC-114 | warning | Too many brain switches; wait a few minutes | Wait a few minutes, then switch again. |
| LAI-SVC-115 | warning | That chat page isn't available from the Portal | Use the chat normally; settings and downloads are done by an operator in the Local AI app. |

### `LAI-GW` — owner: [gateway.md](gateway.md)
> Copied from [gateway.md](gateway.md) §8 (authoritative).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-GW-001 | error | The request body is not valid JSON | Fix the request body; it must be a JSON object |
| LAI-GW-002 | error | The request has no `model` | Add `"model": "<id>"`; `GET /v1/models` lists the ids |
| LAI-GW-003 | error | A parameter isn't supported on this endpoint | Remove it (for example `stream` on embeddings) |
| LAI-GW-004 | error | The request is too large | Send smaller images or audio (limit 8 MB JSON, 25 MB audio) |
| LAI-GW-010 | error | The API key is missing, wrong, revoked or expired | Create a new key at ai.marin.cr → API keys and use it as `Authorization: Bearer lai_…` |
| LAI-GW-011 | warning | Too many wrong keys from this address | Wait 5 minutes and use a valid key |
| LAI-GW-012 | error | This key may not use that model | Use an allowed model, or ask the admin to widen the key |
| LAI-GW-013 | error | The key's owner can no longer use the gateway | Ask the admin to restore access |
| LAI-GW-020 | error | Unknown model | Pick an id from `GET /v1/models` |
| LAI-GW-021 | error | No lab computer can run that model right now | Try later, pick another model, or ask the admin to load it |
| LAI-GW-022 | info | The model is being loaded | Wait about a minute and try again |
| LAI-GW-023 | warning | Every computer serving that model is busy | Try again in a few seconds |
| LAI-GW-024 | error | That endpoint isn't offered | Use chat/completions, completions, embeddings or audio/transcriptions |
| LAI-GW-030 | warning | Too many requests per minute for this key | Slow down, or ask the admin for a higher limit |
| LAI-GW-031 | warning | Too many requests at the same time for this key | Wait for a running request to finish |
| LAI-GW-032 | warning | Today's token allowance for this key is used up | Wait until 00:00 UTC, or ask the admin for a larger budget |
| LAI-GW-040 | error | The gateway couldn't reach the node | Admin: check the node is online and `https://ai-nN.marin.cr/health` answers |
| LAI-GW-041 | error | The node returned an error | Try again; admin: check the node's llama logs |
| LAI-GW-042 | error | The node refused the request (for example the prompt is longer than the model's context) | Shorten the prompt or follow the error message |
| LAI-GW-043 | critical | The cloud's copy of a node's API key is wrong | Admin: press "Refresh status" on that node, or run `sudo aictl cloud resend-key` on it |
| LAI-GW-044 | error | The node didn't start answering in time | Try again; long prompts on big models can take minutes |
| LAI-GW-045 | error | The node stopped in the middle of the answer | Ask again |
| LAI-GW-050 | critical | The gateway can't read the stored node keys | Admin: see LAI-CLD-032 |
| LAI-GW-051 | warning | The gateway is turned off or the site is in maintenance | Try later; admin: check `LOCALAI_GATEWAY_ENABLED` and maintenance mode |
| LAI-GW-052 | error | Unexpected gateway error | Try again; admin: check the API log for the request id |
| LAI-GW-060 | warning | Some usage records couldn't be saved | Admin: check the database; requests themselves were not affected |

### `LAI-DSK` — owner: [desktop.md](desktop.md)
> Copied from [desktop.md](desktop.md) §8 (authoritative).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-DSK-001 | error | This user isn't allowed to control Local AI | Press "Allow this user" and enter the administrator password, then log out and log back in (in a terminal: `sudo usermod -aG localai-operators $USER`) |
| LAI-DSK-002 | warning | You were added to the operators group but haven't logged in again | Log out and log back in once |
| LAI-DSK-003 | error | The agent's key file doesn't exist yet | Press "Start agent". If it keeps happening, reinstall with `install-node.sh` (runbook 05) |
| LAI-DSK-004 | error | The Local AI agent is not running | Press "Start agent" and enter the administrator password |
| LAI-DSK-005 | error | The agent couldn't be started | Press "Start agent" once more; if it fails again, press "Restart the computer". For a repair: `sudo aictl doctor` |
| LAI-DSK-006 | error | ai-desktop and the agent are different versions that don't work together | Update both from the rebuild kit or with `sudo aictl update` |
| LAI-DSK-007 | error | The agent refused this app's login | Press "Make a new token" and enter the administrator password (in a terminal: `sudo aictl token rotate`). Meanwhile use a browser with `aictl ui-code` |
| LAI-DSK-008 | warning | No password keyring to protect the lab key | Accept weaker storage, or set up the GNOME keyring and connect again |
| LAI-DSK-009 | warning | The lab connection code expired or was refused | Press "Connect to the lab" again and enter the new code within 15 minutes |
| LAI-DSK-010 | info | Can't reach the lab | Nothing to fix here; this computer works normally. The Lab tab retries by itself |
| LAI-DSK-011 | warning | The lab connection was removed or expired | Press "Connect to the lab" again |
| LAI-DSK-012 | info | The app blocked a connection to the internet | Nothing to do: this is the privacy protection working |
| LAI-DSK-013 | warning | The system tray isn't available | Turn on "AppIndicator and KStatusNotifierItem Support" in Extensions, then restart ai-desktop |
| LAI-DSK-014 | warning | Couldn't turn on "Start at login" | Check that `~/.config/autostart` is writable, then try again |
| LAI-DSK-015 | critical | The app's security sandbox couldn't start | Reinstall ai-desktop from the .deb (`sudo apt install ./ai-desktop_<version>_amd64.deb`) |
| LAI-DSK-016 | warning | Couldn't check or download an update | Try later, or install the update from the rebuild kit |

### `LAI-INS` — owner: [installer.md](installer.md)
> Copied from [installer.md](installer.md) §8 (authoritative; installer output is terminal-only, English).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-INS-001 | error | Unsupported Ubuntu version or architecture | Install Ubuntu 24.04 or 26.04 LTS (amd64 or arm64). |
| LAI-INS-002 | error | Running inside a container | Run the installer on the real machine. |
| LAI-INS-003 | error | Not run with sudo from a normal user | Run `sudo bash install-node.sh …` from the desktop user's terminal. |
| LAI-INS-004 | error | Another installer is running | Wait for it to finish. |
| LAI-INS-010 | error | NVIDIA GPU found but its driver doesn't work | Accept the driver install and reboot, or continue without GPU. |
| LAI-INS-011 | warning | Secure Boot may block the NVIDIA driver | At the next boot choose "Enroll MOK" and type the password you set. |
| LAI-INS-012 | warning | Less than 8 GB of RAM | Only the cpu-only profile will work well. |
| LAI-INS-013 | error | Not enough free disk space | Free space or use `--yes` to accept a smaller budget. |
| LAI-INS-014 | error | A needed port is in use | Stop the program shown, then re-run. |
| LAI-INS-015 | error | No internet for an online install | Connect to the internet, or use `--from-ark <drive>`. |
| LAI-INS-016 | error | The ark drive lacks files for this computer | Sync the drive on a computer with internet first. |
| LAI-INS-017 | warning | The clock is wrong | Set the date and time, then re-run. |
| LAI-INS-020 | error | This computer already has a different node id | Keep the old id, or uninstall with `--purge-config` first. |
| LAI-INS-021 | critical | The agent download failed its signature check | Download again; if it repeats, don't install and report it. |
| LAI-INS-030 | error | Package installation failed | Read the apt messages above, fix, and re-run. |
| LAI-INS-031 | error | The old llama-qwen service is still using port 8080 | Re-run without `--no-migrate`. |
| LAI-INS-032 | error | Building llama.cpp failed | Read `build.log` in the failed build folder under `/opt/localai/llama.cpp/builds/` (named in the message); run `sudo install-node.sh --repair`. |
| LAI-INS-033 | error | Migration failed and was rolled back | The old service is running again. Read the log and report it. |
| LAI-INS-034 | critical | The agent didn't start | Run `journalctl -u localai-agent -n 50` and `sudo install-node.sh --repair`. |
| LAI-INS-035 | error | Users, folders, permissions or companion programs could not be set up | Read the lines above, fix what they name (disk full, read-only disk, damaged ark drive), and re-run `sudo install-node.sh`. |
| LAI-INS-040 | warning | Lid/power-key settings aren't active | Reboot the computer (don't restart logind). |
| LAI-INS-041 | warning | Sleep targets aren't masked | Run `sudo install-node.sh --repair`. |
| LAI-INS-042 | warning | GNOME idle suspend is still on | Log in as the desktop user and run `sudo install-node.sh --repair`. |
| LAI-INS-043 | warning | Power profile isn't "performance" | Run `powerprofilesctl set performance`. |
| LAI-INS-044 | warning | Wi-Fi power save is on | Run `sudo install-node.sh --repair`; use Ethernet if possible. |
| LAI-INS-050 | error (warning in doctor) | Tailscale serve pattern doesn't match the inventory | Use `--serve-pattern` with the value in the inventory. |
| LAI-INS-060 | warning | The admin PIN wasn't set | Run `sudo aictl pin set`. |
| LAI-INS-070 | info | A reboot is needed to finish the install | Reboot when convenient. |
| LAI-INS-080 | error | Putting the old llama-qwen service back failed | Nothing under /etc/localai was removed. Read the message above, fix it, and run the same --uninstall command again. |
| LAI-INS-081 | error | The uninstaller refused to delete a path it does not own | Nothing was deleted. Report the log at /var/log/localai-install.log. |

### `LAI-KIT` — owner: [rebuild-kit.md](rebuild-kit.md)
> Copied from [rebuild-kit.md](rebuild-kit.md) §8 (authoritative; Spanish fixes are there).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-KIT-001 | error | A source package in the kit is damaged | Run `aictl kit verify --full`; sync the drive on a computer with internet. |
| LAI-KIT-002 | error | The kit doesn't have the needed llama.cpp version | Sync the kit with internet, or choose a commit that is in the kit. |
| LAI-KIT-010 | error | Build tools are missing | Run `sudo aictl kit install-debs`. |
| LAI-KIT-011 | error | The CUDA compiler is missing or doesn't match the driver | Run `sudo aictl kit install-debs`, or rebuild with `--backend vulkan`. |
| LAI-KIT-012 | error | No real Vulkan graphics device was found | Install the graphics driver from the kit, or use `--backend cpu`. |
| LAI-KIT-013 | error | The NVIDIA driver isn't loaded | Restart and choose the previous kernel in "Advanced options", or install the driver from the kit. |
| LAI-KIT-014 | error | Not enough disk space to build | Free 5 GB and try again. |
| LAI-KIT-020 | error | The build failed | Read the build log shown; run `aictl rebuild-llama --dry-run`. |
| LAI-KIT-021 | error | The new build doesn't work; the old one is still in use | Nothing breaks. Report the log. |
| LAI-KIT-022 | retired (v0.1) | The new build failed after switching; the old one was restored (replaced by `LAI-LLM-019`) | No longer used. |
| LAI-KIT-030 | error | The agent source in the kit isn't complete | Sync the kit with internet. |
| LAI-KIT-040 | warning | The rebuild kit is missing parts for this computer | Run `aictl ark sync` with internet, or `aictl ark import` from a good drive. |
| LAI-KIT-041 | warning | The ark drive lacks kit parts for another lab computer | Plug the drive into that computer while online, or sync it here. |
| LAI-KIT-042 | error | The kit is for a different Ubuntu version | Sync the kit after the upgrade, with internet. |
| LAI-KIT-043 | warning | The kit's CUDA version doesn't fit this driver | Sync the kit with internet; meanwhile use the prebuilt or Vulkan. |
| LAI-KIT-044 | critical | The kit's agent files failed their signature check | Don't install them. Use another drive and report it. |
| LAI-KIT-045 | warning | The kit's list of pinned versions is damaged or newer than this agent | Nothing breaks. Sync the catalog with internet; it brings a fresh list. |
| LAI-KIT-046 | warning | The kit's index and guide could not be refreshed | The kit itself is fine. Run `aictl ark sync`; it rewrites them. |
| LAI-KIT-047 | error | Installing from the kit failed | Nothing was replaced. Read the message, then run `aictl kit verify --full` and try again. |
| LAI-KIT-048 | error | The ready-made build doesn't fit this computer | Nothing was changed. Build from the kit source instead: `sudo aictl rebuild-llama --from-kit` (or `rebuild-whisper`). |
| LAI-KIT-049 | error | A build could not be exported to the kit | The kit's other files are untouched. Free some space on the drive, then run `sudo aictl kit export-build` again. |

### `LAI-DRL` — owner: [drill.md](drill.md)
> Copied from [drill.md](drill.md) §8 (authoritative; Spanish fixes are there).

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-DRL-001 | warning (error when the drill fails) | The last drill found problems | Open Status, or run `aictl drill show`, and follow each fix. |
| LAI-DRL-002 | info | The drill waited because a sync was running | Nothing to do. It runs later. |
| LAI-DRL-003 | info | The drill doesn't run in survival mode | Nothing to do. |
| LAI-DRL-004 | info | The drill waited because the AI was in use | Nothing to do. |
| LAI-DRL-005 | info | The drill skipped: battery below 50 % | Plug in the charger. |
| LAI-DRL-006 | warning | No drill in 45 days | Run `aictl drill`. Check that `localai-drill.timer` is enabled. |
| LAI-DRL-010 | error | A setting still points to the internet | Run `aictl doctor`; the details name the setting. |
| LAI-DRL-011 | error | Essential content is missing | Run `aictl ark sync` with internet, or `aictl ark import`. |
| LAI-DRL-012 | error | Stored files failed their check | Run `aictl ark verify --full`. |
| LAI-DRL-020 | error | A survival brain couldn't start | Run `aictl doctor`; choose a smaller brain if it doesn't fit. |
| LAI-DRL-021 | warning | A brain is slower than expected | Check the computer is plugged in and not overheating. |
| LAI-DRL-022 | error | A brain gave wrong answers to a simple test | Run `aictl ark verify --item <model>`; report it if it repeats. |
| LAI-DRL-023 | error | The photo brain didn't read the test image | Run `aictl ark verify --item <model>`; report it. |
| LAI-DRL-024 | error | Survival brains don't fit on this computer | Change the profile's survival models (`aictl profile`). |
| LAI-DRL-030 | error | Library search doesn't work | Run `aictl services restart kiwix`. |
| LAI-DRL-031 | warning | Speech recognition is less accurate than expected | Nothing urgent. A bigger whisper model helps. |
| LAI-DRL-032 | error | Speech recognition failed | Run `aictl services restart whisper`. |
| LAI-DRL-033 | warning | A language has no voice of its own | Install a voice for it with `aictl ark sync`. |
| LAI-DRL-034 | error | The voice failed | Run `aictl doctor`. |
| LAI-DRL-040 | error | Maps don't work | Run `aictl ark verify --item <map>`. |
| LAI-DRL-050 | error | The Portal or chat page doesn't work | Run `sudo install-node.sh --repair`. |
| LAI-DRL-060 | warning | Manuals are missing | Run `aictl ark sync --item docs-bundle`. |
| LAI-DRL-070 | error | The rebuild kit is incomplete for this computer | Run `aictl kit status` and sync the missing parts. |
| LAI-DRL-071 | error | llama.cpp could not be rebuilt offline | Run `aictl rebuild-llama --dry-run` and fix what it lists. |
| LAI-DRL-080 | warning | Laptop power settings aren't right | Run `sudo install-node.sh --repair`, then reboot. |
| LAI-DRL-081 | warning | The clock looks wrong | Set the date and time in Settings. |
| LAI-DRL-090 | error | The drill couldn't restore the normal brain | Press **Switch brain** and choose the usual one. |
| LAI-DRL-091 | warning | Something tried to use the internet during the drill | Report the details; offline use may be affected. |
