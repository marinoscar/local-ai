# 06. Rebuild a node with no internet

Use this when a computer is new, wiped or broken, **there is no internet**, and you have an AI-ARK drive. At the end the computer is a working node: AI, library, maps, voice and manuals, all offline.

> **Before you start**
> - You need: the computer (with its charger), the **AI-ARK drive**, a **USB stick of 8 GB or more** (or one that already has the Ubuntu installer), a keyboard and mouse, and this page (printed, or open on another device).
> - Helpful: the lab's paper notes (node id, PIN), and another Linux computer if you must make the USB stick.
> - The drive should have been **green or yellow** the last time it was synced, and synced at least once on a node of the **same Ubuntu version** (the kit's Ubuntu packages are per version).
> - **Time:** 2 to 4 hours. Ubuntu install 30 to 60 minutes; Local AI install and llama.cpp build 30 to 90 minutes; copying the models and library from the drive about an hour per 100 GB.

> 🛠 Verify when implemented (Epic E5 installer and rebuild kit). Everything here follows [installer.md](../specs/installer.md) and [rebuild-kit.md](../specs/rebuild-kit.md); rehearse it once a year on a spare machine.

**Words used on this page**
- **Kit:** the folder `localai/kit/` on the drive. It holds the Ubuntu packages, GPU driver packages, source code, the agent program, the installer and the catalog.
- **Codename:** the name of the Ubuntu version: `noble` is 24.04, `resolute` is 26.04.
- **Backend:** how the AI uses the graphics chip: `cuda` (NVIDIA), `vulkan` (AMD/Intel) or `cpu` (no graphics chip).

## What can't be fixed offline (read this first)
From [rebuild-kit.md §5](../specs/rebuild-kit.md#5-limits-what-cant-be-rebuilt-offline):
- **A kernel update without a matching NVIDIA module:** boot the previous kernel (Part 6, step 26). This is the most common case.
- **A different Ubuntu version than the kit's:** install the same version the kit was made for (step 2).
- **A GPU newer than the kit's CUDA tools:** rebuild with `--backend vulkan` or `--backend cpu`.
- **A model that needs a newer llama.cpp than any in the kit:** not possible offline. Use another model.
- **Secure Boot with self-built driver modules:** needs one extra screen at boot (MOK, step 14).
- **Dead disk, dead GPU, firmware problems:** hardware. Use another computer.
- **The desktop app on an ARM computer:** use a browser at `http://127.0.0.1:7070`.

---

## Part 1. Check the drive and get the Ubuntu installer

1. **Plug the AI-ARK drive into any Linux computer** (or wait until step 8 if you have none) and open **START-HERE.html** on it.
   - **What you should see:** the drive's colour, "survival capable", the last sync date and catalog version, and links to these manuals.
2. **Find which Ubuntu version the kit is for.** In the **Files** app open `AI-ARK → localai → kit → debs`, or in a terminal:

   ```
   ls /media/$USER/AI-ARK/localai/kit/debs/
   ```

   - **What you should see:** folders like `resolute-amd64` (Ubuntu 26.04, normal PC) or `noble-amd64` (Ubuntu 24.04). **Install that Ubuntu version** in Part 2. NVIDIA driver packages are in `kit/nvidia/<codename>-<arch>-<branch>/`.
   - **If it doesn't work:** no `debs` folder: the drive never got its kit. It can't rebuild a node; find another drive, or wait for internet ([05](05-add-a-node.md)).
3. **If you don't have an Ubuntu USB stick:** a full AI-ARK drive has the installer image in `localai/kit/iso/`. Check it isn't damaged:

   ```
   cd /media/$USER/AI-ARK/localai/kit/iso
   sha256sum -c SHA256SUMS --ignore-missing
   ```

   - **What you should see:** `ubuntu-…-desktop-amd64.iso: OK`.
   - Then open **Startup Disk Creator**, choose that ISO and the USB stick, and press **Make Startup Disk**. ⚠️ This erases the USB stick. Unplug the AI-ARK drive first so you can't pick it by mistake.
   - **If it doesn't work:** no `iso` folder: only `ark-drive-full` drives carry it. Use any Ubuntu USB stick of the right version.

---

## Part 2. Install Ubuntu (offline)

4. **Unplug the AI-ARK drive** during the Ubuntu install, so it can't be erased by mistake.
5. **Boot from the USB stick**: plug it in, turn on the computer, press the boot-menu key while the logo shows (often **F12**, **F9**, **F11** or **Esc**), and choose the USB stick.
6. **Install Ubuntu:** choose **Install Ubuntu**, language, keyboard. At the network step choose **don't connect**. Choose **Erase disk and install Ubuntu**. Laptops: disk encryption (**Use LVM and encryption**) is recommended; write the passphrase on paper ([05 Part 1](05-add-a-node.md) explains the trade-off). Leave "third-party software" unticked if it needs the internet; the kit has the drivers.
   - **Your user:** this becomes the desktop user. **Computer name:** if you are rebuilding an existing node, use its old machine name (for example `hp-linux`); the lab inventory on the drive lists it.
   - **What you should see:** "Installation complete. Restart now." Remove the USB stick, restart, log in.
7. **Plug in the charger.** The installer warns on battery.

---

## Part 3. Install Local AI from the drive

8. **Plug in the AI-ARK drive.** Wait 10 seconds. It appears in the **Files** app as **AI-ARK**, at `/media/<you>/AI-ARK` (`<you>` is your user name).
9. **Open a terminal** (**Ctrl + Alt + T**) and look at the installer first (dry run, changes nothing). Replace `ai-nN` with the node id: the old id if you are rebuilding a node (from your paper notes or the drive's `inventory.local.yaml`), or a new, never-used id.

   ```
   sudo bash /media/$USER/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/$USER/AI-ARK --node-id ai-nN --dry-run
   ```

   - **What you should see:** "source: ark:/media/…/AI-ARK", the detected backend (`cuda`, `vulkan` or `cpu`), the profile, the list of packages it would install **from the kit**, the llama.cpp build, and `[dry-run] would …` lines. Nothing is changed.
   - **If it doesn't work:**
     - `LAI-INS-016` (the drive lacks files for this computer): the message lists what's missing, often the packages for this Ubuntu version (go back to step 2) or the driver packages. It can't continue; sync the drive on a node with internet first.
     - `LAI-INS-001`: unsupported Ubuntu version.
     - `LAI-INS-017` (the clock is wrong): set the date and time in **Settings → System → Date & Time**, then run again.
10. **Install for real:**

    ```
    sudo bash /media/$USER/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/$USER/AI-ARK --node-id ai-nN
    ```

    It works with the network unplugged. In order, it: checks the agent program's signature, installs Ubuntu packages from the kit, installs the companion programs, builds llama.cpp from the kit's source (or reuses a prebuilt one), sets up the services and the API key, applies laptop settings, asks you to **choose an admin PIN**, then starts **copying the models, library and maps from the drive** in the background.
    - **NVIDIA computer without a working driver:** it offers the driver packages from the kit (`LAI-INS-010`). Say **yes**. When it says a reboot is needed, reboot (step 13) and **run the same command again**; it continues where it stopped.
    - **What you should see:** `==> [n/15] …` lines, then `Local AI node ai-nN is installed.`, the brain, `Local UI: http://127.0.0.1:7070`, `Content: import running`, and a **TO DO** list.
    - **If it doesn't work:** the message names the phase and the code. Fix what it says and run the same command again. Common ones: `LAI-INS-021` (the agent program failed its signature check: **don't** continue; use another drive), `LAI-INS-030` (a package couldn't be installed), `LAI-INS-032` (the llama.cpp build failed; Part 5). See [07 → Installer stopped](07-troubleshooting.md#installer-stopped).
11. **Optional: put back the old node's settings** (keeps the same API key, PIN and cloud link). The backup was made with `sudo aictl backup-config --to-ark` and is in the folder `localai/kit/config/<node-id>/` on the drive. List the backups of this node and pick the newest one (the date is in the name, `YYYYMMDDTHHMMSSZ`):

    ```
    sudo ls /media/$USER/AI-ARK/localai/kit/config/ai-nN/
    sudo aictl backup-config --restore /media/$USER/AI-ARK/localai/kit/config/ai-nN/localai-config-ai-nN-<date>.tar.gz
    ```

    - **What you should see:** file names like `localai-config-ai-nN-20261102T150405Z.tar.gz`; then a list of files it will replace, a confirmation question, then the agent restarts.
    - **If it doesn't work:** no folder for this node id: no backup was ever made for it; skip this step (you will enroll again in Part 8). `LAI-CLI-012`: the file can't be read; try an older backup.
12. **Log out and back in** (your user was added to the `localai-operators` group).
13. **Reboot** (laptop settings and a new NVIDIA driver only apply after a reboot):

    ```
    sudo reboot
    ```

14. **Secure Boot screen (only if it appears):** if a blue screen titled **Perform MOK management** shows at boot, choose **Enroll MOK → Continue → Yes**, type the password the installer asked you to set, then **Reboot** (`LAI-INS-011`).

---

## Part 4. Check that it works offline

15. **Wait for the copy from the drive to finish:**

    ```
    aictl ark status
    ```

    - **What you should see:** the internal disk's readiness going up; at the end green or yellow. Leave the drive plugged in until it's done.
    - If the copy stopped (for example because of the reboot) and the internal disk is still red, start it again and wait for it: `aictl ark import --catalog --wait`.
    - **If it doesn't work:** `LAI-SYN-005`: a copy is still running; wait. `LAI-SYN-040`: not enough disk space inside; the most important items are copied first. `LAI-SYN-070`: a copy on the drive is damaged; that item is skipped.
16. **Check the node:**

    ```
    aictl status
    aictl doctor
    ```

    - **What you should see:** a brain loaded, services `✓ llama ✓ kiwix ✓ whisper`, `Internet: none` (normal). The doctor may warn that the cloud and Tailscale aren't reachable; that's fine offline.
    - **If it doesn't work:** follow each fix line. GPU problems (`LAI-HW-001`, `LAI-HW-002`, `LAI-LLM-022`): Part 6.

17. **Check the rebuild kit copied to this computer** (so it can repair itself later, even without the drive):

    ```
    aictl kit status
    ```

    - **What you should see:** every kit part for this computer with `✓`.
    - **If it doesn't work:** `LAI-KIT-040`: some parts are missing; leave the drive plugged in until the copy (step 15) has finished, then check again. `LAI-KIT-042`: the kit is for a different Ubuntu version (step 2).

18. **Try Survival mode:**

    ```
    aictl mode survival
    ```

    Then open `http://127.0.0.1:7070` and try buttons 1 (Chat), 4 (Library) and 5 (Maps) ([01](01-survival-mode.md)).
19. **Optional: print the manuals with the real values:**

    Do this from a copy of the repository (for example `~/git/local-ai`); the command needs the `docs` folder of that copy:

    ```
    cd ~/git/local-ai
    sudo aictl docs render
    ```

    It reads `inventory.local.yaml` (from `/etc/localai/`, else from the drive, else from the current folder) and writes HTML (and PDF if Chromium is installed) to `/srv/localai/docs`, and also to the drive if it is plugged in. It never writes inside the repository copy. It also makes this computer's one-page survival card, `survival-card-<computer-name>.html` (and `.pdf`).
    - **What you should see:** a line starting with `✓ Rendered`, then `Survival card:` and the file name. If it says `LAI-CLI-013`, the real values were not found: plug in the AI-ARK drive or add `--inventory <file>`, and run it again. `LAI-CLI-014` only means no Chromium is installed, so you get web pages but no PDF files; open the pages in a browser and print from there.
    - **If it doesn't work:** `LAI-CLI-016` or `LAI-CLI-015` means the survival card text must be changed by the maintainers ([07](07-troubleshooting.md)). Without a copy of the repository, skip this step: `aictl ark sync --item docs-bundle` makes the manuals on its own.

---

## Part 5. Rebuild llama.cpp (`aictl rebuild-llama`)

The installer already built llama.cpp. Use this later when: the AI service doesn't start after a system update (`LAI-LLM-002`, `LAI-LLM-022`), the build failed during install (`LAI-INS-032`), or you want the commit recorded in `node.yaml` (`llama.commit`). Offline it uses the source in the kit. Spec: [aictl.md §4.23](../specs/aictl.md#423-aictl-rebuild-llama--local-root--3-h).

20. **Check first** (changes nothing; it lists every check with ✓ or ✗ and, if a package is missing, the exact command that installs it from the kit):

    ```
    sudo aictl rebuild-llama --from-kit --dry-run
    ```

    - **What you should see:** only ✓ lines. Fix any ✗ line as it says, then run it again.
    - `--from-kit` makes it take the source code from the kit, so it never tries the internet.

21. **Rebuild and switch:**

    ```
    sudo aictl rebuild-llama --from-kit
    ```

    - **What you should see:** `… Building llama.cpp <commit> for cuda (sm 75), 5 parallel jobs`, then `✓ Build … passed its test (CUDA0: …)` and `✓ Switched to the new build. <model> is loaded again.`
    - The new build is tested before it is used. If the AI doesn't come back with it, the previous build is restored by itself.
    - **If it doesn't work:**
      - `LAI-KIT-002` (the kit doesn't have the needed llama.cpp version): choose a commit the kit has. `ls /media/$USER/AI-ARK/localai/kit/src/` shows `llama.cpp-<commit>.tar.gz` files; run `sudo aictl rebuild-llama --from-kit --commit <commit>`. `LAI-KIT-001` (the source in the kit is damaged): `aictl kit verify --full`, or use another drive.
      - `LAI-KIT-010` (build tools missing) or `LAI-LLM-018` (the build failed; the message names a missing package): install it from the kit with `sudo aictl kit install-debs --packages <package>` (step 29), then run step 21 again.
      - **Faster, if this computer exported its own build before:** `ls /media/$USER/AI-ARK/localai/kit/prebuilt/` shows `llama.cpp-<commit>-<backend>-….tar.gz`; restore it with `sudo aictl rebuild-llama --prebuilt <that file>`. It works only with the same Ubuntu version, processor, backend and graphics chip: otherwise it stops with `LAI-KIT-048`, says what is different and names the files the kit has, and changes nothing. `sudo aictl rebuild-whisper` offers a file that fits exactly by itself.
      - Build fails on an NVIDIA computer (CUDA tools missing or not matching the driver): build for Vulkan or the processor instead: `sudo aictl rebuild-llama --from-kit --backend vulkan` (or `--backend cpu`, slow but always works).
      - `LAI-HW-019` (GPU type not detected): give it, for example `--cuda-arch 75` for an RTX 20xx, `86` for RTX 30xx, `89` for RTX 40xx.
      - Runs out of memory while building: `--jobs 2`.
      - `LAI-LLM-019` (or `LAI-KIT-021` for the speech-to-text build): the new build didn't work; the previous one is still in use. Nothing broke.
22. **Build without switching** (to test first): `sudo aictl rebuild-llama --from-kit --no-activate`. It builds and tests the new version but keeps the running one. Old builds are kept too (2 by default; `--keep <n>` changes it).
23. **Speech-to-text has its own build:** `sudo aictl rebuild-whisper --from-kit` (same idea and flags, including `--prebuilt <file>`; `LAI-SVC-012` asks for it).
24. **Save the working builds into the kit** (so the next rebuild of this computer can skip compiling): `sudo aictl kit export-build`. It also happens by itself after each successful build.

---

## Part 6. GPU problems after a rebuild or an update

25. **Check the driver:** NVIDIA: `nvidia-smi`. AMD/Intel: `vulkaninfo --summary`.
    - **What you should see:** the GPU name and memory. For Vulkan, a device that is **not** `llvmpipe` (`LAI-HW-018` means only the software renderer was found).
26. **NVIDIA stopped working after a kernel update** (`LAI-HW-002`): restart, and in the boot menu choose **Advanced options for Ubuntu**, then the **previous** kernel (the second line, without "recovery mode"). If the menu doesn't show, hold **Shift** (or tap **Esc**) right after the maker's logo. The kit's driver modules match the older kernel.
    - **What you should see:** `nvidia-smi` works again. Run `sudo aictl rebuild-llama --from-kit` if the AI still fails.
27. **Still no GPU:** the computer works on the processor with the small models. `aictl models --loadable` lists what fits.

---

## Part 7. Other offline repairs

28. **Damaged catalog** (`LAI-CAT-001`): copy back the last good one from the kit:

    ```
    sudo aictl catalog restore
    ```

29. **A missing Ubuntu package** (a fix line names one, for example `cmake`, or `LAI-KIT-010` / `LAI-KIT-011`). The kit is a local package source; this installs from it with no internet, after checking every package file:

    ```
    sudo aictl kit install-debs --packages cmake
    ```

    Several packages: separate them with commas (`--packages cmake,ninja-build`). Without `--packages` it installs everything a rebuild needs. With several drives or roots, add `--root <root-id>`. To see what it would install without changing anything, add `--dry-run`.
    - **What you should see:** the packages being checked and installed, then `✓`.
    - **If it doesn't work:** `LAI-KIT-001` (a package file in the kit is damaged): `aictl kit verify --part debs --full`, then use another drive or sync this one with internet. `LAI-KIT-042`: the kit is for a different Ubuntu version (step 2).

30. **The Local AI program itself is broken** (the agent won't start after an update, or `LAI-INS-034`): put back the signed copy from the kit, or rebuild it from the kit's source code. (If even `aictl` is missing, `command not found`, skip to step 31.)

    ```
    aictl verify-release /media/$USER/AI-ARK/localai/kit/bin
    sudo aictl kit install agent
    ```

    - **What you should see:** `✓ Signature OK (Local AI release key). … files checked: all match.`, then the agent restarts.
    - **If it doesn't work:** `LAI-KIT-044` (the kit's agent files failed their signature check): **don't** install them; use another drive. To build it from source instead: `sudo aictl rebuild-agent --from-kit --dry-run` (checks), then `sudo aictl rebuild-agent --from-kit` (`LAI-KIT-030`: the source in the kit is incomplete).

31. **Something in Local AI's own files is broken** (doctor fixes say "install-node.sh --repair"): re-run the installer in repair mode. It never changes the node id, key, PIN, profile or content.

    ```
    sudo bash /media/$USER/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/$USER/AI-ARK --repair
    ```

---

## Part 8. When the internet comes back

32. Switch to online mode (needs the PIN if the node is in Survival mode): `aictl mode online`.
33. Join the tailnet: `sudo tailscale up --ssh=false` ([05 Part 5](05-add-a-node.md)).
34. If you restored the old settings (step 11), the cloud link comes back by itself. Otherwise enroll: create a new credential in the cloud and run `sudo aictl cloud enroll` ([05 Part 6](05-add-a-node.md)).
35. Let the nightly sync bring everything up to date, or run `aictl ark sync --wait` ([08](08-scheduled-sync.md)).

## Part 9. Rotate a vendor apt keyring (maintainer task, needs internet)

The rebuild kit holds the Tailscale and NVIDIA CUDA package repositories. The agent carries their public signing keys and a written-down fingerprint for each one. If a vendor changes its key, the kit build stops with `LAI-SYN-060` ("does not match the fingerprint pinned") until the agent is updated. That is on purpose: the agent never accepts a key it was not told about. Do this on a normal computer with the repo, never on a node in an emergency.

You need: this repo checked out, `gpg`, and internet.

1. Get the vendor's new key and look at its fingerprint.
   - Tailscale: `curl -fsSLo /tmp/ts.gpg https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg`
   - NVIDIA: download `cuda-keyring_1.1-1_all.deb` from `https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/`, run `dpkg-deb -x cuda-keyring_1.1-1_all.deb /tmp/nv`, the key is `/tmp/nv/usr/share/keyrings/cuda-archive-keyring.gpg`.
   - Then run `gpg --show-keys --with-colons --fingerprint <file> | grep '^fpr'`.
   - What you should see: a 40-character fingerprint (the first `fpr` line is the main key).
2. Confirm the fingerprint with two more places, not only the download: the vendor's own documentation or announcement, and `https://keyserver.ubuntu.com/pks/lookup?op=get&search=0x<FINGERPRINT>` (run `gpg --show-keys` on the result). All three must agree. If they do not, stop and ask the owner; do not continue.
3. Make an issue (`gh issue create --template runbook.yml`) and a branch `feat/<issue>-rotate-<vendor>-key`.
4. Copy the new file over `agent/internal/kit/aptkeys/tailscale-archive-keyring.gpg` or `agent/internal/kit/aptkeys/nvidia-cuda-archive-keyring.gpg` (same file name).
5. Put the new fingerprint in `agent/internal/kit/aptkeys/aptkeys.go` (`Pins`) and in the table in `docs/specs/rebuild-kit.md` §3.4. During the overlap, when the vendor signs with both keys, list both fingerprints in the `Pins` entry and say so in the table.
6. Run `cd agent && go test ./internal/kit/aptkeys/ ./internal/sync/...`.
   - What you should see: `ok` for each package. A `does not match its pinned fingerprint` failure means the file and the pin disagree: go back to step 1.
7. Open the pull request, merge it, and let the release workflow publish a new agent. Update the nodes ([08](08-scheduled-sync.md)); the next sync rebuilds the kit packages with the new key.
8. Keep the old key's fingerprint in the table, marked "retired", for 2 years so older kits can still be checked ([rebuild-kit.md](../specs/rebuild-kit.md) §3.4).

If it doesn't work: [07 Troubleshooting](07-troubleshooting.md), the `LAI-SYN-060` entry.
