# 07. Troubleshooting

Use this when something doesn't work, or when you see a code like `LAI-SVC-001`. Find your symptom below, or look the code up in the [index by doctor code](#index-by-doctor-code).

> **Before you start**
> - You need: a terminal (**Ctrl + Alt + T**). Some fixes need `sudo` (your login password) or the admin PIN.
> - **Time:** most fixes take 1 to 10 minutes.
> - In an emergency with no internet, the short version is in [01 Part 8](01-survival-mode.md).

> 🛠 Verify when implemented (all epics). Codes and fixes come from the owner specs; if a fix here and the doctor's own fix line disagree, follow the doctor and report the difference.

**How to read a problem.** Every problem looks like this:

```
✗ The AI model service is not running.  [LAI-LLM-001]
  → Fix: Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama`.
```

Line 1 says what happened and gives the **code** in brackets. Line 2 is the fix. `✗` is an error, `!` a warning, `i` information. Codes never change and are the same in every language.

---

## Step zero: run the doctor

Do this first, for every problem.

1. Run:

   ```
   aictl doctor
   ```

   - **What you should see:** problems grouped by how serious they are, each with its code and fix, then a summary like `41 checks passed · 1 warning · 1 error`.
   - In Spanish: `aictl --lang es doctor`. More detail: `aictl doctor -v`. Only one area: `aictl doctor --area LLM` (areas: `AGT NET HW LLM CAT SYN ARK SVC CLD SEC …`).
2. Do what the first **Fix** line says, then run `aictl doctor` again. Repeat until it's clean or only warnings you understand are left.
3. **If `aictl` itself fails:**
   - `LAI-CLI-001` (can't reach the agent): start it, then look at why it stopped:

     ```
     sudo systemctl start localai-agent
     journalctl -u localai-agent -n 50
     ```

   - `LAI-CLI-002` (your user may not talk to the agent): `sudo usermod -aG localai-operators $USER`, then log out and back in.
   - `command not found`: Local AI isn't installed on this computer, or the link is missing. Try `sudo /opt/localai/bin/aiagent ctl doctor`. Otherwise see [05](05-add-a-node.md) or [06](06-rebuild-a-node-offline.md).
4. **See and control the three services** (AI `llama`, library `kiwix`, speech `whisper`): `aictl services` lists them; `aictl services start <name>`, `aictl services stop <name>` and `aictl services restart <name>` act on one (`aictl restart <name>` is the same as `services restart`). Stopping `llama` in Survival mode needs `--force`, on purpose.
5. **Look at the logs** of one part: `aictl logs --unit llama` (also `agent`, `kiwix`, `whisper`, `sync`, `drill`). Keys and tokens are already removed from them, so they are safe to share.
6. **Desktop app problems:** run `ai-desktop --diagnose` in a terminal. It prints what it checked, with codes and fixes.

---

## The app won't open

**You see:** nothing happens when you open **Local AI**, or it shows an error page, or the tray icon is red.

1. **Use the browser instead, right now:** open `http://127.0.0.1:7070`. The Survival Portal at `http://127.0.0.1:7070/portal` needs no login. For the dashboard, get a login code with `aictl ui-code` and type it.
2. **Read the code on the error page** and do the matching fix:
   - `LAI-DSK-001`: this user isn't allowed yet. Run `sudo usermod -aG localai-operators $USER`, then log out and back in.
   - `LAI-DSK-002`: you were just added to the group; log out and back in once.
   - `LAI-DSK-003`: the agent's token file doesn't exist yet. Press **Start agent**. If it keeps happening, the install didn't finish: run the installer again ([05](05-add-a-node.md)).
   - `LAI-DSK-004`: the Local AI agent isn't running. Press **Start agent** and type your login password.
   - `LAI-DSK-005`: the agent couldn't be started. Run `sudo aictl doctor` and follow it; then `journalctl -u localai-agent -n 50`.
   - `LAI-DSK-006`: the app and the agent are versions that don't work together. Update both: `sudo aictl update` with internet; without internet, check the app's package in the kit and install it:

     ```
     aictl verify-release /srv/localai/kit/desktop
     sudo apt install /srv/localai/kit/desktop/ai-desktop_<version>_amd64.deb
     ```

     `✓ Signature OK` must appear first. If it says the signature or a file doesn't match (`LAI-KIT-044` or `LAI-AGT-010`), **don't** install it; use the copy on the AI-ARK drive (`/media/$USER/AI-ARK/localai/kit/desktop/`) after checking it the same way.
   - `LAI-DSK-007`: the agent refused the app's login. Run `sudo aictl token rotate`, then restart the app.
   - `LAI-DSK-013`: no tray icon. In **Extensions**, turn on "AppIndicator and KStatusNotifierItem Support", then restart the app.
   - `LAI-DSK-015`: the app's security sandbox couldn't start. Reinstall it from the `.deb` (same command as `LAI-DSK-006`).
3. **Nothing at `http://127.0.0.1:7070` either:** the agent is down. `sudo systemctl start localai-agent`, wait 10 seconds, try again. `LAI-AGT-006` means another program uses port 7070: `sudo ss -ltnp 'sport = :7070'` shows which.
   - **What you should see:** the Portal or the login page.
   - The agent starts even when its settings are broken, so the Portal still opens. `aictl doctor` then names the problem: `LAI-AGT-001` (the settings file `/etc/localai/node.yaml` is missing: `sudo cp /etc/localai/node.yaml.bak /etc/localai/node.yaml`), `LAI-AGT-002` (a value in it is wrong: fix the line the details name), `LAI-AGT-003` (the saved state was damaged: the node starts in offline mode, check it with `aictl status`), `LAI-AGT-005` (the `aictl` socket could not be created: `sudo systemctl restart localai-agent`) and `LAI-AGT-014` (the agent restarted more than 3 times in the last hour: `aictl logs --unit agent` shows the last error). If both the socket and port 7070 fail, the agent exits and systemd starts it again after 2 seconds; `sudo journalctl -u localai-agent -n 30` shows why.
   - `LAI-AGT-024` ("the agent is starting"): wait 5 to 30 seconds and try again. Only changes are refused during start-up; reading and the survival button work from the first second.
4. **Still broken:** repair the installation (keeps your key, PIN and content):

   ```
   sudo bash /opt/localai/installers/install-node.sh --repair
   ```

   (If that file is missing, use the copy on the ark drive: `sudo bash /media/$USER/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/$USER/AI-ARK --repair`.)

---

## Chat says error

**You see:** the chat page shows an error, "The AI isn't running", or answers never come.

1. Check what's loaded:

   ```
   aictl status
   aictl services
   ```

   - **What you should see:** `Brain ✓ <model> … loaded` and `llama running ✓`.
2. Match the code:
   - `LAI-SVC-106` / "No brain is running": load one. Portal: **8 Switch brain**. Terminal: `aictl models --loadable`, then `aictl load <model-id>`.
   - `LAI-SVC-102`, `LAI-LLM-001`, `LAI-LLM-004` (the AI service isn't running or answering): `aictl restart llama`. Still failing: `aictl logs --unit llama`.
   - `LAI-SVC-101` (chat is locked outside survival mode): log in (`aictl ui-code`) or start Survival mode.
   - An error right after picking a model in the chat's drop-down: that model isn't loaded. Load it through the agent ([03](03-swap-a-model.md)).
   - `LAI-LLM-012` or `LAI-LLM-021` (no model could start at all): `aictl restart llama`, then `aictl load <default model>`. Library, maps and manuals still work meanwhile.
   - `LAI-LLM-027` (ran out of memory): choose a smaller model.
   - `LAI-LLM-005` (the AI rejects its own key): `aictl restart llama`; if it persists, `sudo aictl key rotate` ([02 Part 7](02-chat-and-api-keys.md)).
   - `LAI-LLM-003` (another program uses port 8080): `sudo ss -ltnp 'sport = :8080'`. If it is `llama-qwen`, the node hasn't been migrated: `sudo aiagent migrate llama-qwen` ([05 Part 10](05-add-a-node.md)).
3. Answers are very slow: `LAI-LLM-022` (running on the processor instead of the GPU, see [GPU not detected after update](#gpu-not-detected-after-update)), `LAI-HW-011` (too hot, improve airflow), `LAI-HW-016` (swap, load a smaller model).

---

## A model won't load

**You see:** `LAI-LLM-009`, `LAI-LLM-010`, `LAI-LLM-011`, `LAI-CAT-005` or `LAI-LLM-008`.

1. List what can load here: `aictl models --loadable`.
2. By code:
   - `LAI-CAT-005` (doesn't fit this computer): choose a model from the list above. Nothing else fixes it.
   - `LAI-LLM-011` (not enough room right now): don't use `--keep-others`, or unload a model first (`aictl unload <model-id>`).
   - `LAI-LLM-008` (files missing or not verified): `aictl download <model-id> --wait` with internet, or `aictl ark import --item <model-id> --wait` from the drive.
   - `LAI-LLM-009` (failed to start; the previous model was restarted): `aictl logs --unit llama` shows why. Usually memory: other programs use the GPU (`LAI-HW-005`, close them or log out of games/browsers with video).
   - `LAI-LLM-010` (too slow to start): models on the ark drive load slower. Copy it inside: `aictl ark import --item <model-id> --wait`.
   - `LAI-AGT-022` (another operation is running): wait and try again.
3. Full explanation of "fits": [03 → What "doesn't fit" means](03-swap-a-model.md#what-doesnt-fit-means).

---

## No library

**You see:** "The library has no books" (`LAI-SVC-001`), "The library is not running" (`LAI-SVC-002`), or search finds nothing.

1. Is there content? `aictl ark status` lists the kiwix items and whether they're verified.
2. By code:
   - `LAI-SVC-001` (no books): plug in the AI-ARK drive; its books join the library within seconds. To copy them inside: `aictl ark import --wait`. With internet: `aictl ark sync --wait`.
   - `LAI-SVC-002` (the library service isn't running): `aictl restart kiwix`.
   - `LAI-SVC-003` (the library program isn't installed): install it from the kit with `sudo aictl kit install kiwix-tools`, then `aictl restart kiwix`. If that fails, repair the installation ([The app won't open](#the-app-wont-open) step 4).
   - `LAI-SVC-004` (one book can't be opened): `aictl ark verify --wait`. The other books still work.
   - `LAI-SVC-005` (the library list couldn't be rebuilt): `aictl restart kiwix`; if it repeats, `aictl doctor`.
3. **Read the library without the agent:** `http://127.0.0.1:8888/library/` (if kiwix still runs), or from the drive alone: [01 Part 9](01-survival-mode.md).

---

## Talk or photo doesn't work

1. **Microphone** (`LAI-SVC-103`): click the lock icon next to the browser's address and allow the microphone, or plug in a headset. `LAI-SVC-104`: use the Local AI app, Firefox or Chromium.
2. **Speech-to-text** (`LAI-SVC-010` model missing, `LAI-SVC-011` not running, `LAI-SVC-012` not built for this computer):
   - `LAI-SVC-010`: `aictl ark import --wait` from the drive, or `aictl ark sync --item whisper-small --wait` with internet.
   - `LAI-SVC-011`: `aictl restart whisper`.
   - `LAI-SVC-012`: `sudo aictl rebuild-whisper`.
   - `LAI-SVC-014`, `LAI-SVC-016`: speak closer, shorter than 2 minutes.
3. **Voice output** (`LAI-SVC-020` program missing: `sudo aictl kit install piper`, or repair the installation as in [The app won't open](#the-app-wont-open) step 4; `LAI-SVC-021`/`LAI-SVC-022` no voice for the language: import or sync a voice). Answers still show as text.
4. **Photos** (`LAI-SVC-040` no vision model can run here; `LAI-SVC-041` its image part is missing: `aictl ark verify --item <model-id> --wait`; `LAI-SVC-108` photo unusable: JPEG or PNG under 30 MB).

---

## Maps don't work

- `LAI-SVC-030` (no map): plug in the drive and `aictl ark import --wait`.
- `LAI-SVC-031` (no labels): `aictl ark import --item map-assets --wait`.
- `LAI-SVC-032` (no place search): `aictl ark import --item places-central-america --wait`.
- `LAI-SVC-033` (GPS silent): move near a window, or set your position by hand (right-click the map → **I am here**).
- `LAI-SVC-034` (a map file is damaged): `aictl ark verify --item <map-id> --wait`.

---

## Battery

**You see:** `LAI-HW-012` (on battery), `LAI-HW-013` (below 20 %), `LAI-HW-014` (below 10 %), `LAI-SVC-111`/`112` in the Portal, or the laptop turned off by itself.

1. Plug in the charger. Check with `aictl status`: `Power ✓ plugged in`.
   - **If it doesn't work:** `cat /sys/class/power_supply/AC*/online` should print `1`. If it prints `0` with the charger plugged in, try another socket, the charger's own light, or another charger of the same watts.
2. In an emergency without power: [01 Part 4 and Part 5](01-survival-mode.md) (small brain, dim screen, solar and car charging).
3. Nightly sync and the monthly drill skip on battery on purpose (`LAI-SYN-003`, `LAI-DRL-005`). Plug in overnight.
4. A laptop server must stay plugged in. See [09](09-laptop-as-server.md).

---

## Forgot PIN

The admin PIN protects deletes, key rotation and leaving Survival mode. Starting Survival mode never needs it.

1. **Too many wrong tries** (`LAI-SEC-003`, `LAI-SEC-004`): wait the time shown (5 minutes after 5 wrong tries), then try again.
2. **Set a new PIN** (doesn't need the old one, needs your login password for `sudo`):

   ```
   sudo aictl pin set
   ```

   - **What you should see:** `New admin PIN (4 to 8 digits):`, `Repeat:`, then `✓ The admin PIN was changed.` Write it on paper and keep it somewhere safe.
   - **No terminal to type in** (for example a setup script): `sudo aictl pin set --pin-stdin` reads the new PIN as one line from standard input, for example from a private file (`< <file>`, then delete the file). The PIN is never accepted on the command line.
   - **If it doesn't work:** "must be run as root" (`LAI-CLI-006`): you forgot `sudo`. Not allowed to use `sudo`: log in as the computer's administrator user.
3. **Forgot the login password too:** restart, hold **Shift** (or tap **Esc**) after the maker's logo, choose **Advanced options for Ubuntu → (recovery mode)**, then **root**. Type `mount -o remount,rw /`, then `passwd <DESKTOP_USER>`, type a new password twice, then `reboot`. With disk encryption you need the disk passphrase first.
4. `LAI-SEC-005` (no PIN set yet): `sudo aictl pin set`.

---

## Disk full

**You see:** `LAI-HW-007` (below the safety limit), `LAI-HW-008` (more than 95 % full, critical), `LAI-SYN-040` (sync skipped items for space), `LAI-AGT-004` (the agent can't write its state).

1. See what uses space:

   ```
   aictl ark status
   df -h / /srv
   ```

2. Free space safely, in this order:
   - Remove old versions and content no longer in the profile (never removes the last verified copy of anything):

     ```
     aictl ark prune --orphans --trash
     ```

   - Delete personal files you don't need (Downloads, videos).
   - Shrink the system logs: `sudo journalctl --vacuum-size=200M`.
   - Delete a model you don't use: `aictl delete <model-id>` (asks to confirm and the PIN).
   - Lower the content budget: in `/etc/localai/node.yaml` set `profile_overrides.budget_bytes` (bytes), then `sudo systemctl restart localai-agent`. Or move content to the ark drive and use it from there.
3. After `LAI-AGT-004`: `sudo systemctl restart localai-agent`.
   - **What you should see:** `aictl doctor` without `LAI-HW-008`.

---

## Sync red

**You see:** the ark is red or yellow, `aictl ark status` shows failed or missing items, or the cloud shows "Sync: ✖ failed".

1. Look at the last run:

   ```
   aictl ark status
   aictl logs --unit sync -n 100
   systemctl list-timers 'localai-*'
   ```

2. By code:
   - `LAI-SYN-001`, `LAI-SYN-002`, `LAI-SYN-003` (skipped: no internet, offline/survival mode, on battery): not a failure. Fix the reason; it runs again tonight.
   - `LAI-SYN-004` (no successful sync for 14 days while online): check the internet, then `aictl ark sync --wait`.
   - `LAI-SYN-031` (download failed after all retries): check the internet; it resumes next run.
   - `LAI-SYN-034` (Hugging Face needs a login or is rate-limiting): wait an hour, or put a Hugging Face token in `/etc/localai/hf-token` (root, mode 0600).
   - `LAI-SYN-040` (not enough space): [Disk full](#disk-full).
   - `LAI-SYN-041`, `LAI-SYN-042` (a survival model, the kit or the manuals don't fit the budget): free space or raise the budget; this is **red** because the node couldn't rebuild itself or survive.
   - `LAI-SYN-020`, `LAI-SYN-027`, `LAI-SYN-028` (a download didn't match its checksum): nothing to do, it's downloaded again. If it repeats, report it.
   - `LAI-SYN-021` (source disagrees with the catalog): report it to the catalog maintainer. The old copy is kept.
   - `LAI-SYN-025` (stored files no longer match, possible disk damage): `aictl ark verify --full --wait`. If files keep failing, copy the ark to another drive.
   - `LAI-SYN-030` (the server no longer has the file): `aictl catalog refresh`.
   - `LAI-SYN-035` (the same item failed 3 runs in a row): `aictl doctor` shows its last error.
   - `LAI-SYN-061` (a tool is missing): `sudo aictl kit install pmtiles` if the message names pmtiles; otherwise repair the installation (`--repair`, see [The app won't open](#the-app-wont-open) step 4).
   - `LAI-SYN-062` (manuals made without the real lab values): copy `inventory.local.yaml` to `/etc/localai/` or plug in the drive, then sync.
   - `LAI-ARK-008` (drive not synced for 30+ days): plug it in overnight on a node with internet.
3. Run it now and watch: `aictl ark sync --wait`. What runs when, and what the colours mean: [08](08-scheduled-sync.md).

---

## The ark drive is not found

1. `aictl ark drives` lists every drive it sees, even unusable ones, and why.
2. By code:
   - Not listed at all: try another USB port or cable; `lsblk -o NAME,SIZE,TRAN,LABEL` should show a `usb` disk with label `AI-ARK`. No label: it isn't formatted as AI-ARK ([04 Part 2](04-ark-drive.md)).
   - `LAI-ARK-005` (plugged in, not mounted): press **Mount** in the app, open it in **Files**, or `aictl ark mount <root-id>`.
   - `LAI-ARK-010` (new drive needs setting up): **Set up this drive**, or `aictl ark init /media/<you>/AI-ARK`.
   - `LAI-ARK-002` (labelled AI-ARK but not ext4): it must be reformatted, which erases it ([04 Part 2](04-ark-drive.md)).
   - `LAI-ARK-003` (read-only): eject, plug in again.
   - `LAI-ARK-004` (filesystem errors): [04 Part 8](04-ark-drive.md), `fsck`.
   - `LAI-ARK-006` (two drives with the same id): [04 Part 8](04-ark-drive.md), `tune2fs -U random`.
   - `LAI-ARK-014` (read/write errors): eject, try another port and cable; if it repeats, replace the drive.
   - `LAI-ARK-009` (can't eject, files in use): switch brain or close the listed programs.

---

## Cloud shows node offline

**You see:** on `https://ai.marin.cr` the node is red "Down", amber "Degraded", or an alert `LAI-CLD-020`, `LAI-CLD-021` or `LAI-CLD-027`.

1. **Is it supposed to be quiet?** Grey "Offline mode (expected)" means the node is in offline or survival mode. The cloud can't bring it back (`LAI-CLD-034`); someone at the computer runs `aictl mode online`. ai-n2 is also "offline" whenever it is booted into Windows.
2. **On the node:**

   ```
   aictl status
   aictl cloud status
   ```

   - `Internet ✗`: fix the network (cable, Wi-Fi, router). `LAI-NET-013`: DNS doesn't work.
   - `LAI-CLD-051` (credential rejected): in the cloud, node → **Keys** → **Replace credential…**, then `sudo aictl cloud enroll` on the node.
   - `LAI-CLD-052` (disabled in the cloud): re-enable it on the cloud's workers page.
   - `LAI-CLD-053` (cloud unreachable): nothing to do on the node; it retries by itself.
   - `LAI-CLD-061` (another computer uses this node id): each computer needs its own `node.id` in `node.yaml`.
3. **Healthy in the cloud but `ai-nN.marin.cr` doesn't answer** (`LAI-CLD-027`, or `502` from the public URL):
   - On the node: `tailscale status` (logged in? `LAI-NET-003` → `sudo tailscale up --ssh=false`) and `tailscale serve status` (listener present? the agent restores it in online mode; `LAI-NET-004` gives the exact command).
   - On the VPS (as root): `tailscale ping <machine>`, then the Pattern A or B `curl …/health` check from [05 step 23](05-add-a-node.md), then `docker exec proxy-nginx nginx -t` and `tail -n 100 /opt/infra/proxy/nginx/logs/error.log`. Pattern B with "upstream SSL certificate verify error": the node's Tailscale certificate lapsed; `LAI-NET-008` on the node means it's being issued, wait a minute.
4. **The laptop went to sleep or dropped Wi-Fi:** [Laptop sleeps or drops off the network](#laptop-sleeps-or-drops-off-the-network).

---

## API key doesn't work (401)

1. Which key? A node key works only on that node (`127.0.0.1:8080` or `ai-nN.marin.cr`); a `lai_` key works only on `https://ai.marin.cr/v1` ([02](02-chat-and-api-keys.md#which-address-and-which-key)).
2. The node key was rotated: get the new one with `aictl key copy` and update the program. OpenCode on the node: [02 step 13](02-chat-and-api-keys.md).
3. Gateway codes: `LAI-GW-010` (key missing, wrong, revoked or expired: create a new one), `LAI-GW-012` (the key may not use that model), `LAI-GW-011` (too many wrong keys from your address: wait 5 minutes), `LAI-GW-043` (the cloud's copy of a node key is wrong: `sudo aictl cloud resend-key` on that node).
4. **`200` without any key** is the dangerous opposite: `LAI-SEC-014`. Run `aictl restart llama` and `aictl doctor` at once; if a public URL is open, disable its vhost on the VPS ([05 step 27](05-add-a-node.md)).

---

## GPU not detected after update

**You see:** `LAI-HW-001`, `LAI-HW-002`, `LAI-HW-003`, `LAI-HW-018`, `LAI-LLM-022` (running on the processor), or answers became very slow after Ubuntu installed updates.

1. **Restart the computer first.** Many updates (kernel, driver) only work after a restart.
2. **NVIDIA:** `nvidia-smi`.
   - Works: run `sudo aictl rebuild-llama` (the AI program may need rebuilding for the new driver), then `aictl doctor`.
   - Fails after a kernel update: boot the **previous kernel** (restart, hold **Shift** or tap **Esc** after the logo, **Advanced options for Ubuntu**, pick the second, older entry). Then `nvidia-smi` again.
   - With internet: `sudo ubuntu-drivers install --gpgpu`, then reboot. Without: the kit's driver packages, see [06 Part 6](06-rebuild-a-node-offline.md).
   - Secure Boot: if a blue **MOK management** screen appears at boot, choose **Enroll MOK** and type the password chosen during the driver install (`LAI-INS-011`).
3. **AMD/Intel (Vulkan):** `vulkaninfo --summary` must show a device that isn't `llvmpipe`. `LAI-HW-018`: install `mesa-vulkan-drivers` (with internet `sudo apt install mesa-vulkan-drivers`; without, from the kit: `sudo aictl kit install-debs --packages mesa-vulkan-drivers`) and restart. `LAI-HW-003`: `/dev/kfd` or `/dev/dri/renderD*` missing; restart and check the `amdgpu` driver loads.
4. **Permissions** (`LAI-HW-004`): `sudo usermod -aG video,render localai`, then `aictl restart llama`.
5. **Wrong backend** (`LAI-HW-017`) or GPU type (`LAI-HW-019`): `aictl profile suggest`; fix `llama.backend` or `llama.cuda_arch` in `/etc/localai/node.yaml`; `sudo aictl rebuild-llama`.
6. Meanwhile the small models still work on the processor: `aictl models --loadable`.

---

## Laptop sleeps or drops off the network

**You see:** `LAI-HW-020` (lid closes → sleep), `LAI-NET-014` (Wi-Fi power saving), `LAI-INS-040` to `LAI-INS-044`, or SSH freezes.

1. `aictl doctor --area HW --area NET`.
2. Fix:`sudo bash /opt/localai/installers/install-node.sh --repair`, then **reboot**. **Don't restart `systemd-logind`** from the desktop; it can freeze the login.
3. Check each setting by hand and run the lid test: [09](09-laptop-as-server.md). Use an Ethernet cable if you can.

---

## Installer stopped

The installer prints the phase and code, and the full log is `/var/log/localai-install.log` (root only: `sudo less /var/log/localai-install.log`). Fix the cause and **run the same command again**: it continues where it stopped.

| Code | Fix |
|---|---|
| `LAI-INS-001` | Install Ubuntu 24.04 or 26.04 LTS (amd64 or arm64) |
| `LAI-INS-003` | Run it as `sudo bash install-node.sh …` from the desktop user's terminal |
| `LAI-INS-010` | Accept the driver install and reboot, or continue without GPU |
| `LAI-INS-013` | Free disk space, or add `--yes` to accept a smaller budget |
| `LAI-INS-014` | A port is in use: stop the program it names |
| `LAI-INS-015` | No internet: connect, or use `--from-ark` ([06](06-rebuild-a-node-offline.md)) |
| `LAI-INS-016` | The drive lacks files for this computer: sync it on a node with internet |
| `LAI-INS-017` | Set the date and time |
| `LAI-INS-020` | This computer already has another node id: keep the old id |
| `LAI-INS-021` | Signature check failed: **don't install**; download again or use another drive, and report it |
| `LAI-INS-030` | Read the apt messages above it, fix, run again |
| `LAI-INS-032` | Build failed: read the build log it names; then `--repair`, or [06 Part 5](06-rebuild-a-node-offline.md) |
| `LAI-INS-033` | Migration failed and was rolled back: the old service runs; read the log and report it |
| `LAI-INS-034` | The agent didn't start: `journalctl -u localai-agent -n 50`, then `--repair` |
| `LAI-INS-050` | Use `--serve-pattern` with the value in the inventory |
| `LAI-INS-060` | Set the PIN: `sudo aictl pin set` |

All codes: [installer.md §8](../specs/installer.md#8-doctor-codes-installer-output-is-terminal-only-english).

---

## Index by doctor code

Each code is `LAI-<AREA>-<NNN>`. The registry of all codes is [doctor-codes.md](../specs/doctor-codes.md); each area's table in its owner spec is the authority.

| Area | Prefix | What it is about | Codes table | Sections on this page |
|---|---|---|---|---|
| Agent core / config | `LAI-AGT` | The Local AI service itself, settings, state, updates | [agent.md §8.1](../specs/agent.md#81-lai-agt--agent-core-and-config) | [Step zero](#step-zero-run-the-doctor), [App won't open](#the-app-wont-open), [Disk full](#disk-full) |
| CLI | `LAI-CLI` | The `aictl` command | [aictl.md §8](../specs/aictl.md#8-doctor-codes-lai-cli) | [Step zero](#step-zero-run-the-doctor) |
| LLM / router | `LAI-LLM` | The AI models (llama-server), builds, migration | [llama-router.md §8](../specs/llama-router.md#8-doctor-codes-lai-llm) | [Chat says error](#chat-says-error), [A model won't load](#a-model-wont-load) |
| Catalog / profile | `LAI-CAT` | The list of models and content, profiles | [catalog.md §7](../specs/catalog.md#7-doctor-codes-lai-cat) | [A model won't load](#a-model-wont-load) |
| Ark sync | `LAI-SYN` | Nightly downloads and checks | [ark-sync.md §8](../specs/ark-sync.md#8-doctor-codes) | [Sync red](#sync-red) |
| Ark drive | `LAI-ARK` | The AI-ARK drive and content folders | [ark-sync.md §8](../specs/ark-sync.md#8-doctor-codes) | [The ark drive is not found](#the-ark-drive-is-not-found) |
| Companion services (001–099) | `LAI-SVC` | Library, speech, voice, maps, vision | [companion-services.md §8](../specs/companion-services.md#8-doctor-codes) | [No library](#no-library), [Talk or photo](#talk-or-photo-doesnt-work), [Maps](#maps-dont-work) |
| Survival Portal (100–149) | `LAI-SVC` | The big-button screen | [survival-portal.md §8](../specs/survival-portal.md#8-doctor-codes-lai-svc-100--149) | [Chat says error](#chat-says-error), [Battery](#battery) |
| Network / Tailscale | `LAI-NET` | Tailnet publishing, internet checks | [agent.md §8.2](../specs/agent.md#82-lai-net--network-and-tailscale) | [Cloud shows node offline](#cloud-shows-node-offline), [Laptop sleeps](#laptop-sleeps-or-drops-off-the-network) |
| Cloud link (node side, 050–069) | `LAI-CLD` | The node's connection to ai.marin.cr | [agent.md §8.5](../specs/agent.md#85-lai-cld-050069--agent-side-cloud-link-client) | [Cloud shows node offline](#cloud-shows-node-offline) |
| Cloud (001–049) | `LAI-CLD` | Alerts and checks in ai.marin.cr | [cloud-app.md §8](../specs/cloud-app.md#8-doctor-codes-lai-cld) | [10](10-lab-operations.md) |
| Gateway | `LAI-GW` | `https://ai.marin.cr/v1` | [gateway.md §8](../specs/gateway.md#8-doctor-codes-lai-gw) | [API key doesn't work](#api-key-doesnt-work-401) |
| Desktop | `LAI-DSK` | The Local AI app | [desktop.md §8](../specs/desktop.md#8-doctor-codes-lai-dsk) | [The app won't open](#the-app-wont-open) |
| Installer | `LAI-INS` | `install-node.sh`, laptop settings | [installer.md §8](../specs/installer.md#8-doctor-codes-installer-output-is-terminal-only-english) | [Installer stopped](#installer-stopped), [Laptop sleeps](#laptop-sleeps-or-drops-off-the-network) |
| Rebuild kit | `LAI-KIT` | Offline rebuild materials and builds | [rebuild-kit.md §8](../specs/rebuild-kit.md#8-doctor-codes-shown-in-the-desktop-english-and-spanish) | [06](06-rebuild-a-node-offline.md) |
| Drill | `LAI-DRL` | The monthly self-test | [drill.md §8](../specs/drill.md#8-doctor-codes-shown-in-the-desktop-english-and-spanish) | [08](08-scheduled-sync.md) |
| Security / keys / PIN | `LAI-SEC` | Tokens, PIN, keys, permissions | [agent.md §8.4](../specs/agent.md#84-lai-sec--security-keys-pin) | [Forgot PIN](#forgot-pin), [API key doesn't work](#api-key-doesnt-work-401) |
| Hardware | `LAI-HW` | GPU, memory, disk, heat, battery | [agent.md §8.3](../specs/agent.md#83-lai-hw--hardware) | [GPU](#gpu-not-detected-after-update), [Battery](#battery), [Disk full](#disk-full) |

A code not listed on this page: look it up in the table for its area and follow its fix. If the fix doesn't help, write down the code, the time, and the output of `aictl doctor -v`, and report it.
