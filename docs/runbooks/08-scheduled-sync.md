# 08. Scheduled sync and the drills

Use this when you want to know what the node does by itself at night, check whether the ark is ready, run a sync now, limit how much internet it uses, or read the monthly self-test. It also has the **quarterly "pull the plug" checklist**.

> **Before you start**
> - You need: a terminal (**Ctrl + Alt + T**). Changing settings needs `sudo`.
> - **Time:** reading the status 2 minutes. A manual sync: minutes to hours. The quarterly drill: about 45 minutes.

> 🛠 Verify when implemented (Epic E3 sync, E9 drill).

**Words used on this page**
- **Sync:** downloading what the profile asks for, checking every file's fingerprint (sha256), and swapping new versions in. It **never deletes the last good copy** of anything.
- **Content root:** a place that holds content: the internal disk (`/srv/localai`, id `internal`) and each AI-ARK drive (id `ark-…`).
- **Timer:** a systemd alarm clock that starts a job at a set time.

---

## Part 1. What runs, and when

| Job | When | What it does | Skips when |
|---|---|---|---|
| **Nightly sync** (`localai-sync.timer` → `localai-sync.service`) | Every night at **03:15**, plus a random delay of up to 45 minutes. If the computer was off, it runs soon after it starts | Refreshes the catalog, plans each content root (internal disk and any plugged-in AI-ARK drive), downloads with resume, verifies, swaps in, spends up to 90 minutes re-checking old files for disk damage, writes the readiness report, tells the cloud | The node is in offline or survival mode (`LAI-SYN-002`), there is no internet (`LAI-SYN-001`), it is on battery (`LAI-SYN-003`), or sync is disabled. Skipped is **not** failed |
| **Monthly drill** (`localai-drill.timer` → `localai-drill.service`) | **First Sunday of the month at 04:30**, plus up to 30 minutes | A self-test that proves everything works with no internet: files, each survival brain, photo question, library search, speech both ways, maps, Portal, manuals, rebuild kit | Survival mode (`LAI-DRL-003`), a sync is running (`LAI-DRL-002`, tries again daily for a week), someone is chatting (`LAI-DRL-004`, waits up to 48 h), battery below 50 % (`LAI-DRL-005`) |

When the timer's drill is skipped (one of the reasons above), it prints `i Drill skipped: <reason>` and the timer does not count it as a failure. The drill never disconnects the network and never changes the mode. While it runs, the agent blocks every connection that isn't to the computer itself, so a feature that secretly needs the internet fails the test.

1. **See the timers:**

   ```
   systemctl list-timers 'localai-*'
   ```

   - **What you should see:** `localai-sync.timer` and `localai-drill.timer` with their **NEXT** and **LAST** times.
   - **If it doesn't work:** a timer missing or `n/a`: `sudo systemctl enable --now localai-sync.timer localai-drill.timer`. No drill for 45 days raises `LAI-DRL-006`.

---

## Part 2. See the results

2. **Readiness of each content root:**

   ```
   aictl ark status
   ```

   - **What you should see:** for each root a colour symbol, a percentage, what's missing, and the last sync, for example:
     `Internal disk  internal  ! 82% ready (180 of 220 GB budget, 273 GB free)` / `missing: wikipedia-es-nopic (12.0 GB)` / `last sync 12 hours ago: ok`.
3. **What the last sync did:**

   ```
   journalctl -u localai-sync -n 50
   aictl logs --unit sync -n 100
   ```

   - **What you should see:** one line per item (`downloaded`, `already up to date`, `failed` with a code) and a summary `✓ Sync finished: 2 downloaded, 14 already up to date, 0 failed.`
4. **Elsewhere:** the desktop app (status and a notification after each sync), the Portal's **7 Status and battery** screen (ark colour), the cloud's node page → **Ark** tab, and on the drive itself `READY.json` and `START-HERE.html`.

---

## Part 3. What green, yellow and red mean

| Colour | Meaning | What to do |
|---|---|---|
| **Green** | Everything the profile wants is here, verified, and recent | Nothing |
| **Yellow** | Everything **essential** is here, but something is behind: a newer version couldn't be downloaded (the old one still works), a less important item is missing, files weren't re-checked in time, space is low, the internal disk had no successful sync for 14 days while online (`LAI-SYN-004`), oran ark drive wasn't synced for more than 30 days (`LAI-ARK-008`) | Fix it this week: plug in, connect to the internet, let it sync overnight |
| **Red** | Something **essential** is missing or damaged: a tier-1 item, a survival model, the rebuild kit, the manuals; or the drive has filesystem errors (`LAI-ARK-004`); ora drive wasn't synced for more than 90 days | Fix it now. Read the codes in `aictl ark status` and [07 → Sync red](07-troubleshooting.md#sync-red) |

- **Age limits** before an item turns yellow: models and library books 180 days, maps 365 days, Ubuntu and driver packages in the kit 60 days, other kit parts 90 days. Models never turn red just from age.
- **"Survival capable"** is a separate yes/no: the survival brains, at least one speech model and voice per Portal language, an essential library book and the map are all verified, on this computer plus any plugged-in drive.
- **The cloud is stricter about ark drives:** it shows a drive amber from 21 days and raises a **critical** alert (`LAI-CLD-022`) at 30 days, so drives get plugged in before they turn yellow locally.
- Exact rules: [ark-sync.md §4.12.2](../specs/ark-sync.md#4122-colour-rules).

---

## Part 4. Run a sync now, and limit bandwidth

5. **See what it would do** (changes nothing):

   ```
   aictl ark sync --dry-run
   ```

6. **Run it and watch:**

   ```
   aictl ark sync --wait
   ```

   - **What you should see:** a progress line per download, then the summary. Ctrl-C asks "Cancel the sync too? [y/N]"; **N** keeps it running in the background.
   - Only one root: `--root internal` or `--root <ark-root-id>`. Only one item: `--item <id>`.
   - **If it doesn't work:** `LAI-SYN-005`: a sync is already running (`aictl ark status` shows it; stop it with `aictl ark sync --cancel`). `LAI-SYN-011`: the node is in offline mode; add `--allow-offline-mode` to use whatever network exists, or `aictl mode online`. `LAI-SYN-010`: refused in survival mode.
7. **Stop a running sync:** `aictl ark sync --cancel`. Half-downloaded files are kept and resume next time.
8. **Re-check every file now** (finds disk damage; long on big drives):

   ```
   aictl ark verify --full --wait
   ```

9. **Limit bandwidth, or change other sync settings.** They are in the `sync:` block of `/etc/localai/node.yaml` ([ark-sync.md §3.3](../specs/ark-sync.md#33-nodeyaml-keys-owned-by-this-spec)):

   ```
   sudo nano /etc/localai/node.yaml
   ```

   | Setting | Default | Example |
   |---|---|---|
   | `bandwidth_limit` | `"0"` (unlimited) | `"20MiB/s"` or `"100Mbit/s"` |
   | `concurrency` | `2` downloads at once | `1` on a slow line |
   | `allow_on_battery` | `false` | keep `false` on laptops |
   | `max_run_hours` | `12` | `6` to stop before morning |
   | `enabled` | `true` | `false` to stop nightly runs |

   Save (**Ctrl + O**, **Enter**, **Ctrl + X**), then apply:

   ```
   sudo systemctl restart localai-agent
   ```

   - **What you should see:** `aictl doctor` with no `LAI-AGT-002` (a mistake in `node.yaml` names the line; the old settings stay in use until it's fixed).
   - To change the **time** of the nightly run: `sudo systemctl edit localai-sync.timer`, type these three lines in the space it offers (the empty `OnCalendar=` clears the old time), then save and close:

     ```ini
     [Timer]
     OnCalendar=
     OnCalendar=*-*-* 01:30:00
     ```

     Check with `systemctl list-timers 'localai-*'`.

---

## Part 5. The monthly drill: run it and read it

10. **Run it now** (up to about 30 minutes; it loads each survival brain in turn):

    ```
    aictl drill
    ```

    - **Short version** (about 2 minutes; skips the full file check and only tests the default brain): `aictl drill --quick`.
    - **Only one check again** after fixing it: `aictl drill --only <check-id>` (check ids such as `drl.files.hash` are listed in [drill.md §4.2](../specs/drill.md#42-checks-in-order)). Add `--only` several times for several checks.
    - **From a script or over SSH without a terminal:** add `--wait` so it waits for the end and gives the real result.

    - **What you should see:** one line per check and a last line, for example:
      `✓ Brain answered in 2.1 s` / `✓ Library search worked` / `✓ Maps tiles served` / `! Speech-to-text slow (8.2 s)` / `✓ Drill passed with 1 warning. Next drill: in 30 days.`
    - **If it doesn't work:** `LAI-DRL-002`, `003`, `004`, `005`: it refused to start (sync running, survival mode, AI in use, battery below 50 %).
11. **Read the last result** without running a new one:

    ```
    aictl drill show
    ```

    - **What you should see:** the date and result of the last run, and one line per check that didn't pass, with its code and fix. An older run: `aictl drill show <run-id>`.

12. **How to read it:**
    - **Pass:** every check passed.Exit code 0.    - **Pass with warnings** (exit 8): everything essential works, something is slow or second-best (for example `LAI-DRL-021` a brain slower than expected, `LAI-DRL-031` speech less accurate, `LAI-DRL-033` a language uses another language's voice). Fix when convenient.
    - **Fail** (exit 9): a check failed. Each failed check has a code and a fix. The most important ones: `LAI-DRL-011` essential content missing, `LAI-DRL-012` stored files damaged, `LAI-DRL-020` a survival brain couldn't start, `LAI-DRL-023` the photo brain failed the test image, `LAI-DRL-030` library search broken, `LAI-DRL-050` Portal or chat broken, `LAI-DRL-060` manuals missing, `LAI-DRL-071` llama.cpp couldn't be rebuilt offline, `LAI-DRL-090` the normal brain wasn't put back (press **Switch brain** and choose it).
    - **"Survival ready: yes"** means every critical check passed or only warned. That's the one that matters in an emergency.
    - `LAI-DRL-091` (something tried to use the internet during the drill): report it; an offline feature may be broken.
    - All codes: [drill.md §8](../specs/drill.md#8-doctor-codes-shown-in-the-desktop-english-and-spanish). The cloud shows the result on the node card ("Drill ✔") and raises `LAI-CLD-024` on failure.

---

## Part 6. The quarterly "pull the plug" drill

Every 3 months, by someone who is **not** the person who set up the node if possible. It tests what software can't: really no network, really on battery, a real restart. **About 45 minutes.** The 17 steps, each with what you should see and what to do if it doesn't work, are in **[01 Survival mode, Appendix, Part B](01-survival-mode.md#appendix-monthly-and-quarterly-checks)** (English and Spanish, printable with the Survival guide, and also in the manuals on the drive). They live there so the page that is printed next to the computer is the only copy. Source: [drill.md §4.6](../specs/drill.md#46-manual-quarterly-pull-the-plug-drill).

In short: run `aictl drill` and check it passes, unplug the network and the charger, restart, press **Survival mode**, test keys **1** to **7**, plug in the AI-ARK drive, reconnect, leave Survival mode, and record the result with `sudo aictl drill record-manual --result pass|fail --notes "…"`. The cloud shows the result on the node card.
