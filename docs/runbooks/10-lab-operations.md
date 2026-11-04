# 10. Running the lab

Use this when you operate the whole lab from `https://ai.marin.cr`: checking node health and alerts, approving catalogs, managing gateway keys, reading the audit log, doing the weekly and monthly checks, and updating agents and llama.cpp on every node, and installing or updating the cloud app itself (Part 9).

> **Before you start**
> - You need: a Google account on the cloud's allowlist. What you can do depends on your role: **viewer** can look; **contributor** and **org admin** can also send everyday commands and create their own API keys; the **lab owner** (system admin) can add and retire nodes, approve catalogs, send destructive commands, manage every key and read the audit log ([cloud-app.md §3.4](../specs/cloud-app.md#34-identity-allowlist-and-permissions)).
> - Internet. The cloud is never needed for a node to work; it's only the remote control.
> - **Time:** weekly check 10 minutes; monthly 30 to 60 minutes; a lab-wide llama.cpp update about an hour per node.

> 🛠 Verify when implemented (Epic E6 cloud, E7 gateway). Page names follow the design in [cloud-app.md §3.11](../specs/cloud-app.md#311-ui-pages-web-appscloudappswebsrclocalai).

---

## Part 1. Dashboard tour

1. **Sign in.** Open `https://ai.marin.cr`, press **Sign in with Google**.
   - **If it doesn't work:** "not allowed": the lab owner must add your exact email to the allowlist (**Admin → Settings → Users & Allowlist → Allowlist**; Part 8).
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

25. **After a catalog PR is merged** and tagged `catalog-<version>`: **Lab → Catalog → Import release…**, type the tag (for example `catalog-2026.11.02-1`) and press **Import**.
    - **What you should see:** "Importing catalog-…", then (within a few seconds) a new row with status **draft** and **checking…** in the Lint column, which turns **✔** (valid) or **✖ N errors**.
    - **If it says "This version already exists."** the cloud already has that version; nothing was changed.
    - **If it doesn't work:** `LAI-CLD-033` ("The catalog checker failed"): the cloud's catalog checker is missing; redeploy the cloud app.
26. Press **Diff vs approved** on the new row to compare with the approved version. Read the changes: new models (do they say a measured `min_vram_mib`?), removed items (nodes keep them as "orphans", never deleted automatically), profile changes. Lines starting with `+` are new, lines starting with `-` are gone.
    - **If the row says ✖ N errors:** press **Edit** on it. Each problem is listed under the text as `CODE file:path message`; click one to jump to its file and line. Fix the text and press **Save draft**: that makes a **new** version (old ones never change) and checks it again by itself.
27. When the status is **valid**, press **Edit** on its row, then **Approve** under the text, and read the dialog: it lists the files changed and the lines added and removed, and asks "Approve <version> for all nodes?". **Push to nodes now** is ticked by default; clear it if the nodes should wait for their nightly sync. Press **Approve**.
    - **What you should see:** "Approved <version>." The old approved row says **superseded**.
28. Optional, later: **Push to nodes** on the approved row asks every Healthy or Degraded node to refresh now ("Sent to N nodes"); the others take it at their next nightly sync.
    - **What you should see:** within a day the header says "N of N nodes on it" and every node card shows the new catalog version.
29. Small edits can be made with **New draft** (a plain text editor with line numbers and a lint panel; it starts from the approved version), but changes made only in the cloud are lost at the next import. Prefer a PR ([03 Part 5](03-swap-a-model.md)). Only a lab administrator sees Import, New draft, Edit, Lint, Approve and Push to nodes; everyone else sees the table and the diffs.

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
    - **If it doesn't work:** if the AI doesn't come back on the new build, it switches back by itself (`LAI-LLM-019`). Report the log in the issue.
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

---

## Part 8. Give someone access

Use this to let a person into the cloud, to change what they may do, or to take their access away. You need to be the **lab owner** (the address set as `INITIAL_ADMIN_EMAIL` when the cloud was deployed; the other roles cannot see the allowlist). Nobody can sign in unless their exact address is on the allowlist; the lab owner's own address is the only exception.

**Which role does what** ([cloud-app.md §3.4](../specs/cloud-app.md#34-identity-allowlist-and-permissions)). A new person starts as **viewer**.

| Role | Who it is for | Can look at the lab | Can send everyday commands (load, unload, sync, restart, logs) | Can create their own API keys | Can add or retire nodes, approve catalogs, send destructive commands, read the audit log, manage every key |
|---|---|---|---|---|---|
| **viewer** (default) | Someone who only needs to look | Yes | No | No | No |
| **contributor** | A family member or power user | Yes | Yes | Yes | No |
| **org admin** | A trusted operator | Yes | Yes | Yes | No |
| **admin** (system, plus org admin) | The lab owner | Yes | Yes | Yes | Yes |

> **Never add a whole domain.** Each entry is one person's exact address (`person@example.test`). A domain, a wildcard or a shared mailbox would let anyone with an address there in, including people who register an address later.

52. **Add the address.** Sign in as the lab owner, open **Admin → Settings → Users & Allowlist** (`/admin/settings/users`), open the **Allowlist** tab, press **Add Email**, type the person's exact Google address, and press the confirm button.
    - **What you should see:** the address appears in the list.
    - **If it doesn't work:** you don't see the **Allowlist** tab: you are not signed in as the lab owner. "Invalid email": check for a typo or a space.
53. **Tell the person** to open `https://ai.marin.cr` and press **Sign in with Google** with that same Google account.
    - **What you should see:** they land on **Lab**. They can look at nodes, models, Ark readiness, alerts and command history, and no action buttons appear. That is the viewer role.
    - **If it doesn't work:** they see "not allowed": the address on the list is not the one they signed in with (a different Google account, or a typo). Fix the entry, then ask them to try again.
54. **Change their role** (only if they need more than looking). Open the **Users** tab, find the person (they appear after their first sign-in), open **Manage roles**, tick **contributor** (or **org admin**), and save. Do not tick **admin** for anyone but the lab owner.
    - **What you should see:** the roles column shows the new role. After they reload the page (or sign in again) they see action buttons such as **Load**, **Sync now** and **Create key**.
    - **If it doesn't work:** the buttons are still missing after a reload: ask them to sign out and in again, so their session picks up the new role.
55. **Take access away.** Remove their address from the **Allowlist** tab (**Remove**) and, to end a session that is already open, switch their account off in the **Users** tab. Then revoke their API keys (Part 5).
    - **What you should see:** they can no longer sign in; the keys show as revoked.
56. **Check the Google side once.** The Google OAuth client allows exactly one redirect address, `https://ai.marin.cr/api/auth/google/callback`, and its consent screen stays **Testing** (with your people listed as test users) or **Internal**. It is never **public**.
    - **If it doesn't work:** Google shows `redirect_uri_mismatch`: the address in the client must match that line exactly, with no trailing slash.

The cloud's own machines (the nodes) never use these roles. A node signs in with its own credential and can only talk to its own node routes ([cloud-app.md §4.2](../specs/cloud-app.md#42-binding-guard-every-apinodesidlocalai-route)).

---

## Part 9. Deploying and updating the cloud

Use this when the cloud app at `https://ai.marin.cr` is installed for the first time, when you move it to a new version, or when you move it to a newer agent release. **The cloud is never needed for a node to work**: if a deploy goes wrong, every node keeps answering locally and only remote control and the gateway pause. Take your time, and do not touch a node to fix the cloud.

> **Owner steps.** Steps 57 to 70 need things only the lab owner has: the DNS zone, the VPS and its login, the Google account and the AWS login. An assistant working in the repository cannot do them. Every place where you replace something is a `<PLACEHOLDER>`: the real values live in `inventory.local.yaml` on your computer and on the ark drive, never in this repository.

> **Before you start**
> - You need: your computer with this repository and an `inventory.local.yaml` that has `public_ip` and `route53_zone_id` filled in; the `aws` command logged in to the account that owns the `marin.cr` zone; a login (SSH, as `root`) to the VPS; the Google account that will be the lab owner (`<ADMIN_EMAIL>`).
> - The VPS already runs the shared proxy (`/opt/infra/proxy`) and the shared Postgres (container `infra-postgres`) from `marin-server-infra` ([overview.md §6](../architecture/overview.md)), has Docker, Git and Node 24, and is reachable on ports 80 and 443.
> - A published agent release. Open <https://github.com/marinoscar/local-ai/releases>, find the newest line called `node-v…` that is not a draft. The part after `node-v` is your `<AIAGENT_VERSION>` (for the release `node-v0.1.0-rc.1` it is `0.1.0-rc.1`).
> - **Time:** about 1 hour the first time; 10 minutes for an update.

> 🛠 Verify when implemented (Epic E6, CLOUD-18). The commands are the design of [cloud-app.md §3.13](../specs/cloud-app.md#313-deploy-laictl-deploy-to-the-vps); record in the issue what differed on the real VPS.

### 9.1 First install

57. **Check that the deploy branch exists.** Open the repository on GitHub, press **Actions**, choose the workflow **cloud-deploy-branch**, and check that the newest run is green. Then open the branch list and look for `deploy/cloud`.
    - **What you should see:** a green run, and a branch `deploy/cloud` whose newest commit is called `deploy: apps/cloud @ <seven characters>`.
    - **If it doesn't work:** no branch yet: press **Run workflow**, keep **target_branch** as `deploy/cloud`, press the green button, and wait for the green mark. A red run: open it; the last step says why (a leak found by the redaction check, or a symlink). Fix it on `main`; do not edit `deploy/cloud` by hand.
    - To rehearse without touching the real branch, run the same workflow with **target_branch** `deploy/cloud-test` first, then delete that scratch branch.
58. **Point the name at the VPS (DNS).** On your computer, in the repository folder:
    ```bash
    scripts/cloud-dns.sh            # dry run: shows the change, sends nothing
    scripts/cloud-dns.sh --apply    # creates or updates the record
    scripts/cloud-dns.sh --verify   # checks the answer (repeat after a few minutes if it says "nothing")
    ```
    The script reads the VPS address and the zone from `inventory.local.yaml` (or from `--ip` and `--zone`). It writes the change to `~/ai-cloud-dns.json`, a private file outside the repository. The record is `ai.marin.cr`, type `A`, time-to-live 300 seconds. It refuses an address that is private, a tailnet address or still a `<PLACEHOLDER>`.
    - **What you should see:** `✓ Route53 applied the change.` and later `✓ ai.marin.cr answers with the VPS address.`
    - **If it doesn't work:** `AWS refused the change`: you are logged in to the wrong AWS account or the zone id is wrong. `not a public address`: the inventory holds the tailnet or LAN address; use the VPS public one. The same by hand, if you prefer: `aws route53 change-resource-record-sets --hosted-zone-id <ROUTE53_ZONE_ID> --change-batch file://$HOME/ai-cloud-dns.json`, then `dig +short ai.marin.cr`.
59. **Create the database and its role on the VPS.** Over SSH, as `root`. The cloud uses its own ordinary role, never the administrator and never the public port:
    ```bash
    docker exec -i infra-postgres psql -U <PG_ADMIN_USER> -v ON_ERROR_STOP=1 <<'SQL'
    CREATE ROLE local_ai_app LOGIN PASSWORD '<DB_PASSWORD>' NOSUPERUSER NOBYPASSRLS;
    CREATE DATABASE local_ai OWNER local_ai_app;
    SQL
    docker exec -i infra-postgres psql -U <PG_ADMIN_USER> -d local_ai -v ON_ERROR_STOP=1 -c 'ALTER SCHEMA public OWNER TO local_ai_app;'
    ```
    Invent a long `<DB_PASSWORD>` (password manager) and keep it for step 61. The repository keeps this SQL here only; the real `marin-server-infra` repository records the role too.
    - **What you should see:** `CREATE ROLE`, `CREATE DATABASE`, `ALTER SCHEMA`.
    - **If it doesn't work:** `role already exists`: the role was made earlier; skip the first line and set its password with `ALTER ROLE local_ai_app PASSWORD '<DB_PASSWORD>';`. `No such container`: find the Postgres container name with `docker ps` and use that name.
60. **Create the Google sign-in.** In the Google Cloud console open **APIs & Services → Credentials → Create credentials → OAuth client ID**, type **Web application**. Under **Authorized redirect URIs** add exactly `https://ai.marin.cr/api/auth/google/callback` (no slash at the end). Copy the **client id** and **client secret** into your password manager. Under **OAuth consent screen** leave the publishing status on **Testing** (add `<ADMIN_EMAIL>` and your people as test users) or **Internal**. Never **In production**.
    - **What you should see:** the client appears in the list with that one redirect address.
    - **If it doesn't work:** later, `redirect_uri_mismatch` on sign-in means the address differs by a character; fix it here (Part 8, step 56).
61. **Install the cloud on the VPS.** Over SSH, as `root`:
    ```bash
    mkdir -p /opt/infra/apps && cd /opt/infra/apps
    git clone --branch deploy/cloud https://github.com/marinoscar/local-ai.git local-ai-cloud-src
    cd local-ai-cloud-src && npm ci --workspace=cli && npm run build --workspace=cli
    node apps/cli/dist/cli.js deploy doctor --domain ai.marin.cr
    node apps/cli/dist/cli.js deploy install --domain ai.marin.cr --ref deploy/cloud --no-version-bump --name local-ai-cloud
    ```
    (`--no-version-bump` because the branch is generated; versions are changed on `main`.) The installer asks for the settings of `.env`. Answer like this, and let it generate the secrets it offers to generate:

    | Question | Answer |
    |---|---|
    | `APP_URL` | `https://ai.marin.cr` |
    | `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | from step 60 |
    | `GOOGLE_CALLBACK_URL` | `https://ai.marin.cr/api/auth/google/callback` |
    | `INITIAL_ADMIN_EMAIL` | `<ADMIN_EMAIL>` (your own Google address: this person becomes the lab owner) |
    | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_SSL` | `infra-postgres`, `5432`, `local_ai`, `local_ai_app`, `<DB_PASSWORD>`, as the installer's text says for the private network |
    | `AIAGENT_VERSION` | `<AIAGENT_VERSION>` from "Before you start" |
    | `JWT_SECRET`, `COOKIE_SECRET`, `SECRETS_ENCRYPTION_KEY` | generated |
    | `TENANCY_MODE`, `JOBS_WORKER_MODE`, `EVENT_BUS_ADAPTER`, `OTEL_ENABLED`, `CORS_ORIGIN`, gateway switches | keep the defaults the installer shows (`single`, `all`, `in-process`, `false`, empty, `true` and `public`). The full list with the reason for each is in [cloud-app.md §3.13](../specs/cloud-app.md#313-deploy-laictl-deploy-to-the-vps) |

    The installer builds the images, starts the stack, writes the `ai.marin.cr` vhost into `/opt/infra/proxy/conf.d/`, asks for the certificate and schedules its renewal.
    - **What you should see:** `deploy doctor` all green before the install; at the end of the install a green summary with the address `https://ai.marin.cr`.
    - **If it doesn't work:** the image build stops with `AIAGENT_VERSION is not set` or a download error: the version is empty or does not name an existing release; fix it in the `.env` of the install folder and run the install (or `deploy update`) again. The certificate step fails: DNS (step 58) has not spread yet; wait 5 minutes and retry. Anything else: keep the output, put it in the issue, and see [07-troubleshooting.md](07-troubleshooting.md).
62. **Back up the encryption key now.** Find the line `SECRETS_ENCRYPTION_KEY=…` in the `.env` file of the install folder (`deploy install` prints that folder at the end; the file is `infra/compose/.env` inside it). Copy the value into your password manager **and** into `inventory.local.yaml` on the ark drive. Do it before anything else.
    - **Why:** the cloud encrypts every node's llama key and every stored credential with this key. Lose it and none of them can be read again (`LAI-CLD-032`): you would have to re-send every key from every node. A copy of the database is useless without it.
    - **What you should see:** two places, outside the VPS, that hold the same value.
    - **If it doesn't work:** you cannot find the line: do not continue; ask in the issue. Never paste the value into the issue or the repository.
63. **Check it is alive.** On any computer:
    ```bash
    curl -s https://ai.marin.cr/api/health/ready
    ```
    - **What you should see:** `{"data":{"status":"ok",…}}`.
    - **If it doesn't work:** `502`: the API container is still starting; wait a minute. A certificate warning: step 61 did not finish the certificate. Other answers: on the VPS run `docker compose logs api --tail 100` in the install folder and put the lines (without secrets) in the issue.
64. **Sign in.** Open `https://ai.marin.cr`, press **Sign in with Google** with `<ADMIN_EMAIL>`.
    - **What you should see:** the **Lab** page with **No nodes yet** and an **Add node** button.
    - **If it doesn't work:** "not allowed": `INITIAL_ADMIN_EMAIL` was typed differently from your Google address; fix `.env` and run `deploy update`. `redirect_uri_mismatch`: step 60.
65. **Check the catalog checker is in the image.** On the VPS, in the install folder:
    ```bash
    docker compose exec api /opt/localai/bin/aictl version --json
    ```
    - **What you should see:** a short JSON text with the agent version you chose.
    - **If it doesn't work:** `no such file`: the image was built without the checker; this shows in the cloud as `LAI-CLD-033`. Set `AIAGENT_VERSION` in `.env` and run `deploy update` (step 68).
66. **Check the three node settings.** In the cloud open **Admin → Settings → Workers** (`/admin/settings/workers`).
    - **What you should see:** `staleHeartbeatSeconds` **45**, `offlineStaleMultiplier` **4**, `offlineRetentionDays` **3650**. The first deploy writes them by itself.
    - **If it doesn't work:** other numbers: set these three and save; **Admin → Settings → Doctor** shows the check "node settings" green afterwards.
67. **Check the gateway paths on the VPS (read only).** The long answers of the `/v1/` gateway need the edge to stream for up to 1800 seconds, and the inner `gateway.conf`. Those come with the gateway work (GW-03) and are not added by this part. Until they exist, nothing is wrong. After GW-03, check:
    ```bash
    grep -rn "1800" /opt/infra/proxy/conf.d/ | grep -i ai.marin.cr
    ls apps/cloud/infra/nginx/app.d/locations/gateway.conf   # in the install folder, from the repository's apps/cloud
    ```
    - **What you should see:** a line with `1800` for `/v1/`, and the file listed.
    - **If it doesn't work:** nothing to fix here; report it on the GW-03 issue.
68. **Record the vhost.** In the private `marin-server-infra` repository, add the new `ai.marin.cr` vhost the deploy wrote into `/opt/infra/proxy/conf.d/`, so a rebuilt VPS gets it back ([overview.md §6](../architecture/overview.md)).
    - **What you should see:** a commit in that repository.
    - **If it doesn't work:** you have no access: ask the owner of that repository.

### 9.2 Updating

An update is two parts: the code lands on `main` (a pull request, with `cloud-ci` green), then the workflow **cloud-deploy-branch** copies it to `deploy/cloud`, then you update the VPS.

69. **Wait for the branch.** After the pull request is merged, open **Actions → cloud-deploy-branch** and wait for the green run (a few minutes). Check that the newest commit of `deploy/cloud` mentions the seven-character code of your merge (the first seven characters of its commit id on GitHub).
    - **What you should see:** `deploy: apps/cloud @ <that code>`.
    - **If it doesn't work:** no new run: `cloud-ci` was red or did not run (it only runs when `apps/cloud`, `packages/ui` or `catalog` changed). The run says "already up to date": nothing changed in the cloud, which is fine.
70. **Update the VPS.** Over SSH, as `root`:
    ```bash
    cd /opt/infra/apps/local-ai-cloud-src
    node apps/cli/dist/cli.js deploy update --name local-ai-cloud --no-version-bump
    ```
    To move to a newer agent release first change `AIAGENT_VERSION` in the install folder's `.env` to the new `<AIAGENT_VERSION>`, then run the `deploy update` line.
    - **What you should see:** a green summary that names the same seven-character code as the branch's newest commit (step 69); `curl -s https://ai.marin.cr/api/health/ready` answers `ok`; the Lab page loads and the nodes' cards come back within a minute (they keep reporting by themselves).
    - **If it doesn't work:** the summary shows an older code: run `git pull --ff-only` in this folder and the `deploy update` line again. The build fails: the old cloud keeps running; read the error, fix it on `main`, wait for the branch (step 69) and run the update again. Nodes are not affected in the meantime. A restore of the whole cloud from a database copy needs the key from step 62.

When the cloud can't be reached at all, nodes show `LAI-CLD-053` and keep working; see [07-troubleshooting.md](07-troubleshooting.md#cloud-shows-node-offline).
