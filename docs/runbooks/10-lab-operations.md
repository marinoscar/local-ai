# 10. Running the lab

Use this when you operate the whole lab from `https://ai.marin.cr`: checking node health and alerts, approving catalogs, managing gateway keys, reading the audit log, doing the weekly and monthly checks, and updating agents and llama.cpp on every node.

> **Before you start**
> - You need: a Google account on the cloud's allowlist. What you can do depends on your role: **viewer** can look; **contributor** and **org admin** can also send everyday commands and create their own API keys; the **lab owner** (system admin) can add and retire nodes, approve catalogs, send destructive commands, manage every key and read the audit log ([cloud-app.md §3.4](../specs/cloud-app.md#34-identity-allowlist-and-permissions)).
> - Internet. The cloud is never needed for a node to work; it's only the remote control.
> - **Time:** weekly check 10 minutes; monthly 30 to 60 minutes; a lab-wide llama.cpp update about an hour per node.

> 🛠 Verify when implemented (Epic E6 cloud, E7 gateway). Page names follow the design in [cloud-app.md §3.11](../specs/cloud-app.md#311-ui-pages-web-appscloudappswebsrclocalai).

---

## Part 1. Dashboard tour

1. **Sign in.** Open `https://ai.marin.cr`, press **Sign in with Google**.
   - **If it doesn't work:** "not allowed": the lab owner must add your exact email to the allowlist (**Admin → Settings → Allowlist**).
2. **Lab** (`/lab`): one card per node.
   - The header: number of nodes, how many online, alerts, and "Gateway: ● N nodes" (how many nodes the gateway can use).
   - Each card: **health** (green Healthy, amber Degraded, red Down, grey "Offline mode (expected)"), **mode** (Online / Offline / Survival), hardware, **loaded models** (★ = default), graphics memory use, **Ark** per content root (internal and drive, with the drive's age), last **Sync** and **Drill**, agent and catalog versions (▲ = behind).
   - Grey is **not** a problem: the node was put in offline or survival mode on purpose and isn't expected to report.
3. **Node page** (press **Open** on a card): header with profile, agent version, llama.cpp build, **Refresh status**, **Go offline…** (lab owner). Tabs:
   - **Models:** state, on disk, fits; **Load**, **Unload**, **Set default**, **Download**; row menu **⋮ → Delete files…** (owner, type the id). See [03](03-swap-a-model.md).
   - **Ark:** each root's tiers, missing/failed items, space, last sync and verify; **Sync now** (with **Preview (dry run)**), **Verify**; the last error code with its fix.
   - **Services:** llama, kiwix, whisper, sync, drill; **Restart** for the first three.
   - **Logs:** choose a unit and line count, **Fetch**. Keys and tokens are removed before logs leave the node.
   - **Keys:** the node key hint, **Rotate key…** (owner; see what breaks in [02 Part 7](02-chat-and-api-keys.md)); the node credential, **Replace credential…**. Full keys are never shown.
   - **Settings:** name, public host, serve pattern, tailnet machine and IP (hidden, **Reveal** is audited), **gateway enabled** and **priority**, catalog pin, **Retire node…** (type the id; ids are never reused).
   - **Go offline** works from the cloud, but bringing the node back online must be done **at the node** (`aictl mode online`). The cloud can never start Survival mode.
4. **Alerts** (`/lab/alerts`): active problems, each with a code and the plain fix, and **Ack**. Alerts clear themselves when the problem is gone. Emails go to people with the operate permission.

   | Code | Alert | What to do |
   |---|---|---|
   | `LAI-CLD-020` | Node stopped reporting | Check it's on and connected ([07 → Cloud shows node offline](07-troubleshooting.md#cloud-shows-node-offline)) |
   | `LAI-CLD-021` | Offline more than a day | Turn it on, or retire it |
   | `LAI-CLD-022` | Ark drive not synced for 30+ days | Plug the drive into that node overnight, or **Sync now** ([04](04-ark-drive.md)) |
   | `LAI-CLD-023` | Last sync failed | Node → **Ark** tab → read the code → fix → **Sync now** |
   | `LAI-CLD-024` | Monthly drill failed | Node page, or `aictl drill show` on it ([08 Part 5](08-scheduled-sync.md)) |
   | `LAI-CLD-025` | Essential content missing | **Sync now**, or import from the drive |
   | `LAI-CLD-026` | Cloud has no key for the node | **Refresh status**; else `sudo aictl cloud resend-key` on the node |
   | `LAI-CLD-027` | Public address not answering | `tailscale serve status` on the node; the VPS vhost ([05 Part 7](05-add-a-node.md)) |
   | `LAI-CLD-028` | Node on an old catalog | **Refresh catalog** on the node |
   | `LAI-CLD-029` | Newer agent available | Part 4 |
   | `LAI-CLD-031` | Node added but never connected | Finish the install, or retire it |

   Full list: [cloud-app.md §8](../specs/cloud-app.md#8-doctor-codes-lai-cld).
5. **Catalog** (`/lab/catalog`): the approved version, how many nodes use it, drafts and old versions. Part 3.
6. **API keys / Usage / Models** (`/gateway/keys`, `/gateway/usage`, `/gateway/models`): Part 5.
7. **Audit** (`/lab/audit`, owner): who did what and when (commands, key reveals, approvals, retirements). Filter by node, action and date.

---

## Part 2. Operator checklists

### Weekly (10 minutes)
8. [ ] **Lab** page: every node Healthy or grey on purpose. No red cards.
9. [ ] **Alerts:** none active, or each one understood and acknowledged.
10. [ ] Every **Ark** chip green or amber; no red. Drives: none older than 21 days.
11. [ ] **Sync** shows ok (or "skipped (mode)" on purpose) on every node.
12. [ ] **Gateway** header shows the expected number of nodes; **Usage** has no surprise spikes (a leaked key?).
13. [ ] Laptop nodes: plugged in (no `LAI-HW-012` on their cards).

### Monthly (30 to 60 minutes)
14. [ ] After the first Sunday: every node shows **Drill ✔**. Investigate any warning ([08 Part 5](08-scheduled-sync.md)).
15. [ ] Plug each **AI-ARK drive** into an online node overnight; next day its age is 0 days and its colour green ([04 Part 6](04-ark-drive.md)).
16. [ ] **Catalog:** every node on the approved version (no ▲). Approve pending drafts that passed lint (Part 3).
17. [ ] **Agents:** all on the latest version, or a reason why not (Part 4).
18. [ ] **API keys:** revoke keys of people who left and keys unused for months; check expiry dates.
19. [ ] **Audit:** skim for anything unexpected (key reveals, deletes, retirements).
20. [ ] **VPS:** certificates are renewing (`echo | openssl s_client -connect ai-n1.marin.cr:443 -servername ai-n1.marin.cr 2>/dev/null | openssl x509 -noout -dates`, and the same for each node).
21. [ ] Update [architecture/lab-inventory.md](../architecture/lab-inventory.md) if anything changed (placeholders only).

### Every 3 months
22. [ ] Run the "pull the plug" drill on at least one laptop node ([08 Part 6](08-scheduled-sync.md)), and update the battery table in its printed [01](01-survival-mode.md).
23. [ ] Re-check every file on each drive: `aictl ark verify --root <root-id> --full --wait` ([04](04-ark-drive.md)).
24. [ ] Re-print the Survival card and [01](01-survival-mode.md) if anything changed (from a copy of the repository: `cd ~/git/local-ai && sudo aictl docs render`, then print from `/srv/localai/docs`).

---

## Part 3. Approving a catalog

The catalog says which models and content exist and how to verify them. Nodes only accept a catalog that passes lint, and enrolled nodes take the one the cloud has **approved**.

25. **After a catalog PR is merged** and tagged `catalog-<version>`: **Lab → Catalog → Import release…**, type the tag (for example `catalog-2026.11.02-1`).
    - **What you should see:** a new row with status **draft**, then the **Lint** column turns ✔ (valid) or ✖ with problems.
    - **If it doesn't work:** `LAI-CLD-033`: the cloud's catalog checker is missing; redeploy the cloud app.
26. Press **Diff** to compare with the approved version. Read the changes: new models (do they say a measured `min_vram_mib`?), removed items (nodes keep them as "orphans", never deleted automatically), profile changes.
27. Press **Approve** and confirm "Approve <version> for all nodes?".
28. Optional: **Push to nodes** so they refresh now; otherwise they take it at their next nightly sync.
    - **What you should see:** within a day every node card shows the new catalog version.
29. Small edits can be made in **New draft** (a plain text editor with a lint panel), but changes made only in the cloud are lost at the next import. Prefer a PR ([03 Part 5](03-swap-a-model.md)).

---

## Part 4. Updating agents

The agent update is signed and checked; the previous version is kept, and if the new one doesn't come up healthy within about a minute, the old one is put back by itself (`LAI-AGT-009`, `LAI-CLD-015`).

30. **Check one node first** (prefer ai-n3). On the node:

    ```
    sudo aictl update --check
    ```

    - **What you should see:** `i Version X is available (you have Y).` The version comes from the newest `node-v<version>` release.
31. **Update it:**

    ```
    sudo aictl update
    ```

    - **What you should see:** `… Downloading and checking the signature`, then `✓ Updated to X. Everything is running.` Models keep running during the update.
    - **If it doesn't work:** `LAI-AGT-009`: it rolled back; nothing is broken, report the version. `LAI-AGT-010` (signature or checksum failed): **don't** install it by hand; report it.
32. **Then the others,** from the node or from the cloud (node page → update agent command, lab owner). Check each card shows the new version and no alerts. Ark laptops may have `agent.update` removed from their allowed cloud commands on purpose; update those at the keyboard.
33. **Desktop app:** releases are tagged `desktop-v<version>` (agent releases are `node-v<version>`, catalogs `catalog-<version>`). Updates are off by default. On nodes with internet, menu **⋮ → Check for updates**. Offline, check the `.deb` in the kit, then install it:

    ```
    aictl verify-release /srv/localai/kit/desktop
    sudo apt install /srv/localai/kit/desktop/ai-desktop_<version>_amd64.deb
    ```

    - **What you should see:** `✓ Signature OK (Local AI release key). … files checked: all match.`, then apt installs it.
    - **If it doesn't work:** a hash or signature mismatch (`LAI-KIT-044` or `LAI-AGT-010`): **don't** install it; report it and use a drive that checks clean.
34. Afterwards, plug each AI-ARK drive in once so its kit carries the new agent ([04](04-ark-drive.md)).

---

## Part 5. Gateway keys and usage

35. **Create a key** for a person or program: **API keys → Create key**: name, models (empty = all), expiry. Copy the `lai_…` key shown once ([02 Part 3](02-chat-and-api-keys.md)).
36. **Service keys** (owner) for systems like Open WebUI: kind **service**, may have no expiry and higher limits.
37. **Revoke** a key: **Revoke** on its row. It stops working on the next request.
38. **Usage:** requests and tokens per day, model or node. Only metadata is stored, never prompts or answers.
39. **Models (owner):** per model, enable or disable it on the gateway, add aliases, allow **on-demand** loading (the gateway may load it on a node that has it, waiting up to the set time), and set preferred nodes.
40. **A node shouldn't receive gateway traffic** (maintenance, a laptop on the road): node page → **Settings** → turn **gateway enabled** off. Or put it in offline mode.

---

## Part 6. Updating llama.cpp across the lab

Every node builds its own llama.cpp for its own GPU. All nodes should run the **same pinned commit**, which the rebuild kits on the drives keep the source for. Router behaviour is re-verified on each new commit ([llama-router.md §9.3](../specs/llama-router.md#93-manual-tests-on-a-real-node)).

41. **Pick the commit** and open an issue for the update. Check the catalog's models all run on it (new model formats sometimes need a newer commit; old ones rarely break).
42. **Test on one node** (ai-n3), with internet so the source can be fetched:

    ```
    sudo aictl rebuild-llama --commit <commit> --dry-run
    sudo aictl rebuild-llama --commit <commit> --no-activate
    ```

    - **What you should see:** the dry run lists every check with ✓ (fix any ✗ first); then `✓ Build <commit>-cuda passed its test (CUDA0: …)`. Nothing is switched yet.
    - **If it doesn't work:** `LAI-LLM-018`: the build failed; the message shows the log. The running build is untouched.
43. **Switch it** and run the router checks (R1 to R8 in [llama-router.md §9.3](../specs/llama-router.md#93-manual-tests-on-a-real-node)): health without key, `401` without key on `/v1/models`, the preset ids, the default loaded at boot, old aliases, an unloaded model refused, swapping models.

    ```
    sudo aictl rebuild-llama --commit <commit>
    aictl version
    aictl drill
    ```

    - **What you should see:** `✓ Switched to the new build. <model> is loaded again.`, `llama.cpp <commit>-<backend>` in `aictl version`, and the drill passing.
    - **If it doesn't work:** if the AI doesn't come back on the new build, it switches back by itself (`LAI-LLM-019`, `LAI-KIT-022`). Report the log in the issue.
    - The new build is also saved into the kit of every writable content root by itself (`sudo aictl kit export-build` does it again by hand), so the drives can restore it with `aictl rebuild-llama --prebuilt`.
44. **Then each other node,** one at a time, with the same `--commit`. ai-n1 builds `vulkan`, ai-n2 and ai-n3 `cuda` (ai-n2 is compute 8.9, ai-n3 7.5; detected automatically). Use `--jobs 2` on a node short of memory. Check its card in the cloud afterwards.
45. **Record the commit:** set `llama.commit` in each node's `/etc/localai/node.yaml` if `rebuild-llama` didn't, so a later offline rebuild uses the same one, and note it in [architecture/lab-inventory.md](../architecture/lab-inventory.md) §2.
    > 🛠 Verify when implemented (Epic E5): [rebuild-kit.md](../specs/rebuild-kit.md) says the build records `node.yaml: llama.commit` and the cloud writes the lab's pinned commits (`pins.json`) into the catalog release so every drive keeps that source. Check both happened.
46. **Refresh the drives:** plug each AI-ARK drive into an online node overnight so its kit gets the new source tarball. Next morning `aictl kit status --root <root-id>` shows every part `✓` for every lab node (no `LAI-KIT-041`), and `aictl ark status` shows the drive green.

---

## Part 7. Retiring a node

47. Move clients off it (Open WebUI connections, scripts using `ai-nN.marin.cr`).
48. Node page → **Settings → Retire node…**, type the id. Its credential is revoked, its key deleted and the gateway stops using it. The id is never reused.
49. On the VPS, remove its vhost, certificate and DNS record ([source/vps-ai-nodes-setup.md §10](../source/vps-ai-nodes-setup.md#10-removal-and-rollback)).
50. On the computer, if it leaves the lab: `sudo aictl cloud unenroll`, or uninstall ([installer.md §4.4](../specs/installer.md#44-uninstall)).
51. Update `inventory.local.yaml` and [architecture/lab-inventory.md](../architecture/lab-inventory.md).
