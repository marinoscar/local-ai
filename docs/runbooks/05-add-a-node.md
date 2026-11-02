# 05. Add a node to the lab

Use this when you turn a new computer into a lab node, publish it at `https://ai-nN.marin.cr`, add it to the gateway, or migrate one of today's nodes (ai-n1, ai-n2, ai-n3) from the old `llama-qwen.service`.

> **Before you start**
> - You need: the computer, power, **an Ethernet cable if possible**, internet, a USB stick of 8 GB or more, an admin account on `https://ai.marin.cr` (`lab:admin`), and root access on the VPS (`prod-server`) for Part 7.
> - Real values (tailnet IPs, VPS IP, Route 53 zone) are in `inventory.local.yaml`. This page shows placeholders like `<VPS_PUBLIC_IP>`. **Never paste real values into this repo.**
> - **Time:** Ubuntu install 30 to 60 minutes; installer 30 to 90 minutes (it builds llama.cpp); VPS publishing 30 minutes; first content sync overnight.
> - No internet at all? Use [06-rebuild-a-node-offline.md](06-rebuild-a-node-offline.md) instead.

> 🛠 Verify when implemented (Epic E5 installer, E6 cloud Add-node wizard, E7 gateway). The VPS steps (Part 7) already work today; they are the manual path the wizard mirrors.

In this page the new node is **ai-n4** on a machine called **dell-linux**. Use the next free id (ids are never reused) and your machine's real name.

**Words used on this page**
- **Node id:** the lab name, `ai-n<N>`.
- **Machine name / hostname:** the computer's own name, also its name on the tailnet (for example `dell-linux`).
- **Tailnet / Tailscale:** the lab's private encrypted network between the VPS and the nodes.
- **VPS:** the rented server on the internet that hosts `ai.marin.cr` and forwards `ai-nN.marin.cr` to the nodes.
- **vhost:** one nginx configuration file for one web address.
- **Pattern A / B:** how the node shares its AI port on the tailnet. **B** (`--https=443`, used by ai-n2 and ai-n3) is the default for new nodes. **A** (`--tcp=18080`, used by ai-n1) is the older way.

---

## Part 1. Install Ubuntu

1. **Make the USB installer.** On any Ubuntu computer, download the **Ubuntu Desktop 24.04 or 26.04 LTS** ISO from `https://ubuntu.com/download/desktop` (or use `localai/kit/iso/` on a full AI-ARK drive). Open **Startup Disk Creator**, choose the ISO and the USB stick, press **Make Startup Disk**.
   - **What you should see:** "Installation complete".
2. **Boot from the USB stick.** Plug it in, restart, and press the boot-menu key while the logo shows (often **F12**, **F9**, **F11** or **Esc**). Choose the USB stick.
   - **If it doesn't work:** enter the BIOS setup (often **F2** or **Del**) and allow USB boot.
3. **Install.** Choose **Install Ubuntu**, your language and keyboard, a wired or Wi-Fi network, then **Erase disk and install Ubuntu**.
   - **Disk encryption (LUKS) is recommended for laptops** (a stolen laptop then reveals nothing). In the disk step choose the advanced options and **Use LVM and encryption** (the wording varies between Ubuntu versions). Choose a strong passphrase and **write it down on paper**, kept apart from the laptop.
   - **Trade-off:** with encryption, the computer stops at the passphrase prompt after every power cut, until someone types it. For an always-on desktop server that must come back by itself (like ai-n1), decide on purpose.
   - Tick **Install third-party software for graphics and Wi-Fi hardware** (needed for NVIDIA).
   - **Your user** becomes the node's desktop user. **Computer name** = the machine name (for example `dell-linux`).
   - **What you should see:** "Installation complete. Restart now." Remove the USB stick and restart.
4. **First login, then update:**

   ```
   sudo apt update && sudo apt full-upgrade -y
   sudo reboot
   ```

5. **NVIDIA computers only: check the driver.**

   ```
   nvidia-smi
   ```

   - **What you should see:** a table with the GPU name and memory. If it fails, the installer offers to install the driver in Part 3 (then a reboot).
6. **Plug in the charger and, if you can, an Ethernet cable.** Laptops: also set the BIOS option **Power on after AC restore** if it exists ([09](09-laptop-as-server.md)).

---

## Part 2. Create the node in the cloud (Add node wizard)

> 🛠 Verify when implemented (Epic E6).

7. Open `https://ai.marin.cr` → **Lab** → **Add node**. Fill in step 1:
   - **Node id:** the suggested next free id (for example `ai-n4`).
   - **Name:** for example `Dell Precision (ai-n4)`.
   - **Public host:** `ai-n4.marin.cr` (filled in for you).
   - **Serve:** **Pattern B** unless you have a reason for A.
   - **Tailnet name:** the machine name (`dell-linux`). **Tailnet IP:** leave it empty for now; add it in Part 5 (node page → **Settings**).
   - **Profile hint:** your best guess (`small-8gb`, `cuda-12gb`, `max-128gb`, `cpu-only`); the installer confirms it.
   Press **Create node**.
   - **What you should see:** step 2 with the **node credential** `nod_…` **shown once**, and the install command.
8. Press **Copy** next to the credential and keep it somewhere private for the next 30 minutes (a password manager). You'll paste it in Part 6. Note the **installer checksum (sha256)** shown by the wizard.

---

## Part 3. Run the installer

The installer turns Ubuntu into a node: users and folders, packages, llama.cpp built for this GPU, the agent and services, the API key, laptop hardening, Tailscale, the admin PIN, and the first sync. It is safe to run again; finished parts are skipped. Spec: [installer.md](../specs/installer.md).

9. **Download and check it** (on the new computer). The installer comes from an agent release, whose tag is `node-v` plus the version (for example `node-v0.4.1`). Use the tag shown by the wizard in place of `node-v<version>` (never `main`):

   ```
   cd ~
   curl -fsSL https://github.com/marinoscar/local-ai/releases/download/node-v<version>/install-node.sh -o install-node.sh
   sha256sum install-node.sh
   ```

   - **What you should see:** a 64-character value. It must equal the checksum in the wizard. If it doesn't, **stop** and don't run it.
10. **Dry run** (shows everything it would do, changes nothing):

    ```
    sudo bash install-node.sh --node-id ai-n4 --dry-run
    ```

    - **What you should see:** `[dry-run] would …` lines for each phase, the detected backend (`cuda`, `vulkan` or `cpu`), the suggested profile, laptop hardening steps (laptops), and a summary.
    - **If it doesn't work:** the message names a phase and a code, for example `LAI-INS-001` (unsupported Ubuntu), `LAI-INS-013` (not enough disk), `LAI-INS-014` (a port is in use). See [07 → Installer stopped](07-troubleshooting.md#installer-stopped).
11. **Install for real.** Add `--serve-pattern A` only if you chose Pattern A.

    ```
    sudo bash install-node.sh --node-id ai-n4
    ```

    - It asks you to **choose an admin PIN** (4 to 8 digits), twice. You'll need it to delete things or leave Survival mode. **Write it down** and keep it with the ark drive's paper copy of [01](01-survival-mode.md), not on the drive itself.
    - NVIDIA without a working driver: it offers to install it; say yes, reboot when told, and run the same command again.
    - **What you should see:** `==> [n/15] <phase>` lines, then `Local AI node ai-n4 is installed.`, the brain, local UI address, the API key hint `ab12…ef90`, and a **TO DO** list with a **VPS CHECKLIST**. Keep this terminal open or copy the text: it's also in `/var/log/localai-install.log`.
    - **If it doesn't work:** it stops with the phase and code. Fix what it says and run the same command again: it continues where it stopped. See [07 → Installer stopped](07-troubleshooting.md#installer-stopped).
12. **Reboot** (laptop power settings only apply after a reboot; never restart logind by hand, see [09](09-laptop-as-server.md)):

    ```
    sudo reboot
    ```

13. **Log in again** and check:

    ```
    aictl status
    aictl doctor
    ```

    - **What you should see:** `Node ai-n4 …`, a brain loading or loaded, services `✓`. The doctor ends with `… checks passed`. "Internet ✓" and "Cloud" not linked yet is normal.
    - **If it doesn't work:** `LAI-CLI-002`: log out and back in once (your user was just added to the `localai-operators` group).

---

## Part 4. Laptop hardening (automatic)

14. On laptops the installer has already: ignored the lid and power keys, masked sleep, turned GNOME idle suspend off, set the **performance** power profile, turned Wi-Fi power saving off (three layers), and installed SSH. **Check it** after the reboot:

    ```
    aictl doctor --area HW --area NET
    ```

    - **What you should see:** no `LAI-INS-040` to `LAI-INS-044`, `LAI-HW-020` or `LAI-NET-014` problems. Full manual checks and the lid test: [09-laptop-as-server.md](09-laptop-as-server.md).

---

## Part 5. Join the tailnet

15. **Log in to Tailscale** (once):

    ```
    sudo tailscale up --ssh=false
    ```

    - **What you should see:** a URL. Open it on any device, sign in to the lab's Tailscale account and approve the machine `dell-linux`.
16. **Check:**

    ```
    tailscale status
    tailscale ip -4
    ```

    - **What you should see:** this machine and the VPS `prod-server` in the list. The second command prints this node's tailnet IP (`100.…`). **That address is secret for this repo:** write it only into `inventory.local.yaml` (Part 9) and into the cloud (node page → **Settings** → **Tailnet IP**).
17. **Check the node publishes itself on the tailnet** (the agent does this in online mode; the installer never does):

    ```
    tailscale serve status
    ```

    - **What you should see:** Pattern B: `https://dell-linux.<TAILNET>.ts.net` → `http://127.0.0.1:8080`. Pattern A: TCP `18080` → `127.0.0.1:8080`.
    - **If it doesn't work:** run `aictl doctor --area NET`. `LAI-NET-003`: Tailscale isn't logged in (step 15). `LAI-NET-004`: the details give the exact command to run.

---

## Part 6. Enroll in the cloud

18. **Store the node credential** from Part 2. It asks for it without showing it; paste and press **Enter**:

    ```
    sudo aictl cloud enroll
    ```

    - **What you should see:** `Node credential (nod_…):` (nothing appears while you paste), then `✓ Connected to https://ai.marin.cr as ai-n4. The node shows up on the Nodes page.`
    - **Without a terminal prompt** (for example from a script): put the credential in a private file and feed it on standard input, so it never appears on the command line: `sudo aictl cloud enroll --credential-stdin < <file>`, then delete the file. The credential is never accepted as an argument.
    - **If it doesn't work:** `LAI-CLD-051`: the credential was rejected (typo, or it was revoked); in the cloud open the node → **Keys** → **Replace credential…** and enroll again. `LAI-CLD-053`: the cloud isn't reachable; it's saved and will connect by itself. `LAI-CLD-004`: the credential was made for another node id.
19. **Check:**

    ```
    aictl cloud status
    ```

    In the wizard, steps **6. Node enrolled** and **7. llama key received by the cloud** turn ✔ by themselves.
20. **Delete the copy of the credential** from wherever you kept it.

---

## Part 7. Publish the node on the VPS

The installer printed this checklist with the right pattern. The wizard's step 3 shows the same steps with **Check** buttons. Source: [source/vps-ai-nodes-setup.md §6](../source/vps-ai-nodes-setup.md#6-adding-a-new-node). Run the VPS commands **as root on the VPS**.

> 🛠 Verify when implemented: phase 2 will automate steps 22 to 27 (**Publish automatically**, [cloud-app.md §4.10](../specs/cloud-app.md#410-later-automated-node-vhostcert-publishing-phase-2-not-in-v1)). Until then, do them by hand.

**Rule:** the node must require its API key **before** it is published. The installer guarantees that (`LAI-SEC-014` would show otherwise).

21. **On the node:** check llama answers locally:

    ```
    curl -s http://127.0.0.1:8080/health
    ```

    - **What you should see:** `{"status":"ok"}`.
22. **DNS.** In AWS Route 53, zone `marin.cr` (`<ROUTE53_ZONE_ID>`), create record `ai-n4.marin.cr`, type **A**, value `<VPS_PUBLIC_IP>`, TTL **300**, simple routing. Then check from any computer:

    ```
    dig +short ai-n4.marin.cr
    ```

    - **What you should see:** `<VPS_PUBLIC_IP>`. Wizard: **2. DNS → Check**.
23. **VPS → node over the tailnet** (on the VPS):

    ```
    tailscale ping dell-linux
    ```

    Then, **Pattern B:**

    ```
    curl -i --resolve dell-linux.<TAILNET>.ts.net:443:<TAILNET_IP_AI_N4> https://dell-linux.<TAILNET>.ts.net/health
    ```

    **Pattern A:**

    ```
    curl -i http://<TAILNET_IP_AI_N4>:18080/health
    docker exec proxy-nginx wget -q -O- http://<TAILNET_IP_AI_N4>:18080/health
    ```

    - **What you should see:** `pong`, then `HTTP/… 200` with `{"status":"ok"}`.
    - **If it doesn't work:** go back to step 17 on the node.
24. **Bootstrap HTTP vhost** (lets Let's Encrypt check the name). Create `/opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf`:

    ```nginx
    server {
        listen 80;
        listen [::]:80;

        server_name ai-n4.marin.cr;

        location /.well-known/acme-challenge/ {
            root /var/www/certbot;
        }

        location / {
            return 301 https://$host$request_uri;
        }
    }
    ```

    ```
    docker exec proxy-nginx nginx -t
    docker exec proxy-nginx nginx -s reload
    echo "ai-n4-ready" > /opt/infra/proxy/webroot/.well-known/acme-challenge/ai-n4-test
    curl -fsS http://ai-n4.marin.cr/.well-known/acme-challenge/ai-n4-test
    rm /opt/infra/proxy/webroot/.well-known/acme-challenge/ai-n4-test
    ```

    - **What you should see:** `syntax is ok`, `test is successful`, then `ai-n4-ready`. Wizard: **3. Bootstrap HTTP vhost → mark done**.
25. **Certificate** (certbot):

    ```
    docker run --rm \
      -v /opt/infra/proxy/letsencrypt:/etc/letsencrypt \
      -v /opt/infra/proxy/webroot:/var/www/certbot \
      certbot/certbot:latest certonly \
      --webroot -w /var/www/certbot \
      -d ai-n4.marin.cr \
      --email <ADMIN_EMAIL> \
      --agree-tos \
      --no-eff-email
    ls -l /opt/infra/proxy/letsencrypt/live/ai-n4.marin.cr/
    ```

    - **What you should see:** "Successfully received certificate", and `fullchain.pem` and `privkey.pem` in the folder. Renewal is automatic (`shared/renew-all-certs.sh`). Wizard: **4. Certificate → Check**.
26. **Final HTTPS vhost.** Copy the closest existing node and edit it: `ai-n2.marin.cr.conf` for Pattern B, `ai-n1.marin.cr.conf` for Pattern A.

    ```
    cp /opt/infra/proxy/nginx/conf.d/ai-n2.marin.cr.conf /opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf
    nano /opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf
    ```

    Change: the header comment, both `server_name` lines, both `ssl_certificate` paths, the `proxy_pass` IP, and (Pattern B) `proxy_ssl_name` and `Host`. **Keep:** the `Authorization` pass-through, `proxy_buffering off`, `proxy_cache off`, `Connection ""`, `client_max_body_size 100m`, timeouts `15s/600s/600s`. The `location /` block should end up like this:

    **Pattern B:**

    ```nginx
        location / {
            proxy_pass https://<TAILNET_IP_AI_N4>:443;

            proxy_ssl_server_name on;
            proxy_ssl_name dell-linux.<TAILNET>.ts.net;
            proxy_ssl_verify on;
            proxy_ssl_verify_depth 3;
            proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;

            proxy_http_version 1.1;
            proxy_set_header Host dell-linux.<TAILNET>.ts.net;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto https;
            proxy_set_header Authorization $http_authorization;

            proxy_buffering off;
            proxy_cache off;
            proxy_set_header Connection "";

            proxy_connect_timeout 15s;
            proxy_read_timeout 600s;
            proxy_send_timeout 600s;
        }
    ```

    **Pattern A:** the same, but the first lines are only:

    ```nginx
            proxy_pass http://<TAILNET_IP_AI_N4>:18080;
            proxy_http_version 1.1;
            proxy_set_header Host $host;
    ```

    (no `proxy_ssl_*` lines). Use the **IP**, not the tailnet name, in `proxy_pass`: a failing name lookup at nginx start would take down every site on the VPS.

    ```
    grep -n "ai-n\|proxy_pass\|proxy_ssl_name\|ts.net" /opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf
    docker exec proxy-nginx nginx -t
    docker exec proxy-nginx nginx -s reload
    ```

    - **What you should see:** only `ai-n4` names and the new IP in the grep output; `test is successful`. Wizard: **5. Final HTTPS vhost → Check**.
27. **Validate** (from any computer):

    ```
    curl -i https://ai-n4.marin.cr/health
    curl -s -o /dev/null -w '%{http_code}\n' https://ai-n4.marin.cr/v1/models
    read -rsp "Node API key: " LLAMA_API_KEY; echo
    curl -s -o /dev/null -w '%{http_code}\n' https://ai-n4.marin.cr/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
    unset LLAMA_API_KEY
    ```

    (Get the key on the node with `aictl key copy`.)
    - **What you should see:** `200` with `{"status":"ok"}`; then **`401` without the key**; then **`200` with the key**.
    - **If it doesn't work:** `200` without the key means the node is open to the world: disable the vhost at once (`mv` it out of `conf.d/` and reload) and run `aictl doctor` on the node (`LAI-SEC-014`). `502`: see [07 → Cloud shows node offline](07-troubleshooting.md#cloud-shows-node-offline).
28. **Commit the vhost** in `marin-server-infra` (that repo is private; real IPs are allowed there):

    ```
    cd /opt/infra
    git add proxy/nginx/conf.d/ai-n4.marin.cr.conf
    git diff --cached --check
    git commit -m "Add AI node ai-n4"
    git push origin main
    ```

---

## Part 8. Add it to the gateway

> 🛠 Verify when implemented (Epic E7).

29. In the cloud, node page → **Settings**: check **Public host**, **Serve pattern**, **Tailnet machine**, **Tailnet IP**; set **gateway enabled** and a **priority**. Save.
30. Wizard step **8. Gateway test → Check**.
    - **What you should see:** ✔, and the banner "ai-n4 is part of the lab".
31. From any computer, with a `lai_` key ([02 Part 3](02-chat-and-api-keys.md)):

    ```
    curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $(cat ~/.config/localai/gateway-key)"
    ```

    - **What you should see:** ai-n4's loaded model in the list. A chat request for that model returns the header `x-localai-node: ai-n4` when ai-n4 is chosen.
    - **If it doesn't work:** the node must be healthy, online, enrolled, have its key in the cloud (`LAI-CLD-026`: run `sudo aictl cloud resend-key` on the node) and answer on its public URL (`LAI-CLD-027`).
32. **Open WebUI:** nothing to do once it uses the gateway ([02 Part 5](02-chat-and-api-keys.md)). Before E7, add a connection for `https://ai-n4.marin.cr/v1` with the node key ([source/vps-ai-nodes-setup.md §7](../source/vps-ai-nodes-setup.md#7-open-webui-connections)).

---

## Part 9. Update the lab inventory

33. **Real values, private:** add the node to `inventory.local.yaml` on the owner's machine (id, public host, machine, hardware, backend, profile, `tailnet_ip`, `lan_ip`, `serve_pattern`). Copy it to the ark drive by plugging the drive in (sync copies it), or to `/etc/localai/inventory.local.yaml` on owner nodes.
34. **Redacted values, in this repo** (an issue and a PR, [AGENTS.md](../../AGENTS.md)):
    - `inventory.example.yaml`: add an `ai-n4` entry with placeholders (`<TAILNET_IP_AI_N4>`, `<LAN_IP_AI_N4>`).
    - [architecture/lab-inventory.md](../architecture/lab-inventory.md) §1: add a row (public endpoint, machine name, hardware, backend, profile, pattern, model, status), with placeholders for IPs.
    - Run `scripts/check-redaction.sh` before committing. It fails on any tailnet IP, VPS IP or key.
35. **Back up the node's settings** onto the ark drive: `sudo aictl backup-config --to-ark`. It is saved in `localai/kit/config/<node-id>/` on the drive ([04](04-ark-drive.md) step 16).
36. **Leave it overnight** online and plugged in for the first sync. Next morning: `aictl ark status` should be green or yellow ([08](08-scheduled-sync.md)).

---

## Part 10. Migrating an existing node (ai-n1, ai-n2, ai-n3) from `llama-qwen.service`

Today's nodes run a hand-made `llama-qwen.service`. The migration swaps it for `localai-llama.service` **in place**: same port 8080, same API key, same Tailscale Serve, same public URL, and the old model name keeps working as an alias. **No client needs to change.** The AI is down only while the model reloads (usually 10 to 30 seconds). Spec: [llama-router.md §4.9](../specs/llama-router.md#49-migration-from-llama-qwenservice).

> **Before you start**
> - The owner is present. Pick a quiet time.
> - The catalog must contain the node's current model with the **exact sha256** of the file on disk. If it doesn't, the dry run stops with `LAI-LLM-017` "Model file not in the catalog": add it first ([03 Part 5](03-swap-a-model.md)).
> - ai-n2: boot it into **Ubuntu**, not Windows.

> 🛠 Verify when implemented (Epics E2, E5). Rehearse on ai-n3 first and record the result in the PR.

37. **Write down how it works today** (to compare afterwards):

    ```
    systemctl cat llama-qwen.service
    tailscale serve status
    ```

38. **Run the installer as a dry run** (Part 3 step 9 to download it):

    ```
    sudo bash install-node.sh --node-id ai-n3 --dry-run
    ```

    Use `--node-id ai-n1 --serve-pattern A` on ai-n1. ai-n2 and ai-n3 are Pattern B.
    - **What you should see:** a migration of `llama-qwen.service`, the llama.cpp build for this commit and GPU, hardening steps already done on laptops ("already done"), and no changes made.
39. **Install for real.** The installer installs the agent, then hands over to the migration (`aiagent migrate llama-qwen`), which checks everything **before** stopping the old service:

    ```
    sudo bash install-node.sh --node-id ai-n3
    ```

    To look at the migration plan alone first: `sudo aiagent migrate llama-qwen --dry-run`.
    - **What you should see:** the migration report: downtime (for example 14 s), model id and old alias, the key fingerprint, serve listeners **unchanged**, and **clients to update: none**.
    - **If it doesn't work:**
      - `LAI-LLM-017` "settings differ": the old unit's flags differ from the catalog. Read the diff. If the catalog's values are right, re-run the migration with `sudo aiagent migrate llama-qwen --accept-catalog-flags`; otherwise fix the catalog first. This is expected on **ai-n2**, whose flags were never written down.
      - If anything fails after the switch, the migration **rolls back by itself**: `llama-qwen.service` runs again as before and the installer stops with `LAI-INS-033`.
      - `LAI-LLM-029` (interrupted): run `sudo aiagent migrate llama-qwen` again; it continues.
40. **Check the result:**

    ```
    systemctl is-enabled llama-qwen localai-llama
    readlink /etc/llama-qwen/api-key
    tailscale serve status
    curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/v1/models
    curl -s -H "Authorization: Bearer $(sudo cat /etc/localai/api-key)" http://127.0.0.1:8080/v1/models | jq '.data[] | {id, aliases}'
    ```

    - **What you should see:** `masked` and `enabled`; `/etc/localai/api-key`; the same serve status as step 37; `401`; the catalog id with the old alias (for example `qwen3.5-9b-q4km` with alias `qwen3.5-9b`).
    - From another computer: `https://ai-n3.marin.cr/v1/models` with the **same key as before** → `200`. On ai-n3, `opencode run "say hi"` still works with no config change.
41. **Per-node clean-up:**
    - **ai-n3:** it also has an old, undocumented `--tcp=18080` listener. The doctor reports it as `LAI-NET-005`. Remove it: `sudo tailscale serve --tcp=18080 off`.
    - **ai-n1:** Pattern A stays. The `GGML_VK_SUBALLOCATION_BLOCK_SIZE` setting is carried over into `node.yaml` (`llama.extra_env`). Check `aictl doctor` shows no `LAI-LLM-015`.
    - **ai-n2:** record the captured flags in [architecture/lab-inventory.md](../architecture/lab-inventory.md) §2.
    - The old `~/llama.cpp` folder and `~/models` are left in place (model files become links to `/srv/localai/models/…`). Don't delete them until the node has run well for a few weeks.
42. **Then continue with Part 5** (if Tailscale needs it), **Part 6** (enroll) and **Part 8** (gateway). Part 7 is already done for existing nodes.

### Rolling back a migration
43. **Go back to the old service** (any time after the migration; the API key stays the current one):

    ```
    sudo aiagent migrate llama-qwen --rollback
    ```

    Add `--restore-files` to also move the model files back to their old folders.
    - **What you should see:** `llama-qwen.service` running again, `401` without and `200` with the key on `http://127.0.0.1:8080/v1/models`, Tailscale Serve unchanged. The agent keeps running and reports `LAI-LLM-016` (old service active) until you migrate again.
44. **Remove Local AI completely and restore the old service:**

    ```
    sudo bash install-node.sh --uninstall --restore-legacy
    ```

    Models and content in `/srv/localai` are kept unless you add `--purge-content`.
