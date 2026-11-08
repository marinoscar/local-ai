# 04. The AI-ARK drive

Use this when you buy, format, fill, plug in, eject, store or refresh the portable **AI-ARK drive**, or when you want to use it to serve or copy content on any node.

> **Before you start**
> - You need: a node with internet (for the first fill), a USB 3 SSD, and a terminal (**Ctrl + Alt + T**).
> - Formatting **erases the whole drive**. Unplug every other USB disk first. Formatting, repairing and fixing a copied drive are in the menu (`sudo aictl`, entry 6); the raw commands are only a last resort (Appendices A and B).
> - **Time:** formatting 10 minutes. First fill: many hours (leave it overnight, plugged into power). Later refreshes: minutes to an hour.

> 🛠 Verify when implemented (Epic E3 ark sync and drive; E8 for the desktop buttons).

**What the drive is.** An ext4 disk with the label **AI-ARK** and a folder `localai/` with the same layout as a node's internal content folder: models, library (ZIM files), maps, speech, the rebuild kit and the manuals. At the top it has **START-HERE.html** (opens in any browser) and, if the owner allows it, `inventory.local.yaml` (the lab's real addresses). Details: [ark-sync.md](../specs/ark-sync.md).

**It is not encrypted, by design.** Anyone who finds it can use it in an emergency, and can read everything on it. Don't put personal secrets on it.

---

## Part 1. Buying and choosing a drive

1. **Buy a USB 3 SSD** (solid-state, not a spinning hard disk), **2 TB** recommended, **1 TB** minimum. A USB-C drive with both a USB-C and a USB-A cable or adapter fits every computer in the lab.
2. **Buy two** if you can. Keep one at home and one somewhere else.
3. Avoid: spinning hard disks (slow and break when dropped), cheap no-name flash sticks (slow, wear out), and "secure" drives that need Windows software to unlock.
4. Any brand name is fine. Write the purchase date on a label on the drive.

**Smaller drive?** It still works. The drive's profile is `ark-drive-full` by default; when it doesn't fit, sync fills the most important things first (tier 1) and skips the rest (`LAI-ARK-011`, information only). You can choose a smaller profile in Part 3.

---

## Part 2. Format a new drive as AI-ARK

> ## ⚠️ FORMATTING ERASES THE WHOLE DRIVE
> - **Unplug every other USB disk and memory stick first**, so the only disk on offer is the new one.
> - The menu never offers this computer's own disk, a disk with swap or Local AI data, or a disk that is open. It asks for the admin PIN and for the word `ERASE` before it erases anything.
> - Don't use exFAT, NTFS or FAT32: they can't keep Linux permissions, so the rebuild kit wouldn't work. The menu always uses ext4.

How it works in detail: [ark-sync.md §4.17](../specs/ark-sync.md#417-formatting-a-new-ai-ark-drive). If the menu cannot run at all, use [Appendix A](#appendix-a-format-a-drive-by-hand-last-resort).

5. **Plug in only the new drive.** If a window opens for it, or it appears in **Files**, eject it there first: press the ⏏ next to its name.
   - **What you should see:** the window closes. The drive stays plugged in.
   - **If it doesn't work:** nothing happens when you plug it in: that is fine, go on.

6. **Open the menu as administrator.** Press **Ctrl + Alt + T**, type `sudo aictl` and press **Enter**. Type your login password if it asks (nothing shows while you type). Choose **6** (*Offline content and the AI-ARK drive*), then **Format a new drive as AI-ARK (erases it)**.
   - The typed command is `sudo aictl ark format`; it does exactly the same.
   - **What you should see:** `Disks that can be erased and made an AI-ARK drive:` with your drive on a numbered line (its name, size and `usb`), and under `Not offered:` the other disks with the reason, for example `this computer's own disk (it holds /)`.
   - **If it doesn't work:** `Run it with sudo` (`LAI-CLI-006`): you typed `aictl` without `sudo`. Your drive is under `Not offered`: `open at …` means the desktop opened it, eject it (step 5) and try again; `a disk inside this computer` means it is not a USB disk. No disk at all (`LAI-ARK-016`): try another USB port or cable, wait 10 seconds, try again.

7. **Type the number of your drive** and press **Enter**. Check that the name and the size are those of the drive you bought.
   - **What you should see:** `Erasing a disk needs the admin PIN.` and `Admin PIN:`.
   - **If it doesn't work:** a number that is not in the list, or only **Enter**, stops with `Nothing was changed.` Start again at step 6.

8. **Type the admin PIN** and press **Enter** (nothing shows while you type).
   - **If it doesn't work:** a wrong PIN shows how many tries are left. After three wrong PINs nothing was changed; ask the person who set the PIN up. No PIN set yet (`LAI-SEC-005`): set one first with `sudo aictl pin set`.

9. **Read the disk once more and type `ERASE`** in capital letters, then press **Enter**.
   - **What you should see:** `Everything on this disk will be erased:` with the disk's name and size, then the question.
   - **If it doesn't work:** anything other than `ERASE` (also `erase`) stops with `Nothing was changed.` That is the safe way out if you are not sure.

10. **Wait, and don't unplug the drive.** Each step prints a line: erasing, creating the partition, formatting (several minutes on a big drive), mounting, setting it up.
    - **What you should see:** `✓ The new AI-ARK drive is ready (ark-3f9c0a12, mounted at /media/root/AI-ARK).` The `ark-…` value is the drive's **root id**. Write it on the drive's label.
    - **If it doesn't work:** `LAI-ARK-017` (a disk tool failed): the message says what was left. Unplug the drive, plug it in again and repeat from step 6. If it fails again, use [Appendix A](#appendix-a-format-a-drive-by-hand-last-resort). Pressing **Ctrl + C** here does not stop it: it finishes so the drive is not left half done.

11. **Press Enter** to go back to the menu, then choose **List the connected drives**.
    - **What you should see:** `AI-ARK  ark-3f9c0a12  /media/root/AI-ARK  ext4  … free  ✓ ready`.

12. **Set it up** (only for a drive that shows `uninitialized`, `LAI-ARK-010`: one formatted by hand or with another tool; the menu of step 6 already did this):

    ```
    aictl ark drives
    aictl ark init /media/<you>/AI-ARK
    ```

    Use the mount path `aictl ark drives` shows. The desktop app does the same with the **Set up this drive** button.
    - **What it does:** it makes a folder called `localai` on the drive, with the empty sub-folders the drive needs, and one small file inside it called `ROOT.json` (the drive's name card). Then the computer writes `START-HERE.html` at the top of the drive (Part 9). It never erases, formats or deletes anything, and it only works on a drive that is still empty (`uninitialized`).
    - **What you should see:** `init` asks you to type the drive's id (the `ark-…` value it shows) to confirm; anything else changes nothing, and `--yes` does not answer it. After `init`, `aictl ark drives` shows the drive `✓ ready`.
    - **If it doesn't work:** `unusable` with `LAI-ARK-002`: it isn't ext4; format it (step 6). `LAI-ARK-006`: two drives have the same id (a cloned drive); see Part 8, step 29. The command refuses (and changes nothing) if the drive is already `ready` or the `localai` folder already has files in it: that drive was set up before, so use it as it is (Part 4). `LAI-ARK-003` (the drive is read-only), `LAI-ARK-014` (a read/write error) or `LAI-SYN-040` (no space): fix that first (Part 8), then press **Set up this drive** again; it is safe to repeat.

---

## Part 3. First fill

13. **Optional: choose a smaller profile** for a small drive (needs the PIN or `sudo`):

    ```
    aictl ark drives set-profile <root-id> <profile>
    ```

    Profiles: `ark-drive-full` (everything, default), or a node profile such as `small-8gb` to carry only what that node needs.

14. **See what it will download** (nothing is changed):

    ```
    aictl ark sync --root <root-id> --dry-run
    ```

    - **What you should see:** a list of items with sizes and a total, for example "Will download 900 GB".

15. **Start the fill.** The node must be in **online** mode, on internet, plugged into power. Leave it overnight.

    ```
    aictl ark sync --root <root-id> --wait
    ```

    - **What you should see:** one line per item, for example `… Downloading wikipedia-es-nopic  ████░░░░░░  41%  4.9/12.0 GB  18 MB/s  6 min left`, then `✓ Sync finished: … downloaded, … failed.` Models already on this node are copied locally instead of downloaded again.
    - You can close the terminal: the sync continues inside the agent. Check with `aictl ark status`. To stop it: `aictl ark sync --cancel` (half-done files are kept and resume next time).
    - **If it doesn't work:** `LAI-SYN-010` or `LAI-SYN-011`: the node is in survival or offline mode; switch to online. `LAI-SYN-031`: a download failed after retries; run the same command again later, it resumes. `LAI-SYN-040`: the drive is full. More in [07 → Sync red](07-troubleshooting.md#sync-red).

16. **Back up this node's settings onto the drive** (includes secrets; the file is readable by root only):

    ```
    sudo aictl backup-config --to-ark
    ```

    - **What you should see:** `✓ Saved …/localai/kit/config/<node-id>/localai-config-<node-id>-<date>.tar.gz (… files, includes secrets — keep it private).` The drive keeps the last 5 backups of each computer in `localai/kit/config/<node-id>/`. [06](06-rebuild-a-node-offline.md) step 11 restores it.
    - **If it doesn't work:** `LAI-CLI-006`: you forgot `sudo`. `LAI-CLI-012`: the drive is full or read-only (the message names the file).

17. **Check the rebuild kit on the drive** (the part that rebuilds a computer with no internet):

    ```
    aictl kit status --root <root-id>
    ```

    - **What you should see:** each kit part (packages, driver packages, source code, agent program, tools) with `✓` for this computer and for every other lab computer that synced this drive.
    - **If it doesn't work:** `LAI-KIT-040` (parts missing for this computer) or `LAI-KIT-041` (parts missing for another lab computer): sync the drive overnight again, on that computer if the code names it.

---

## Part 4. Plugging in and reading the colours

18. **Plug it into any node.** Wait about 10 seconds. The node finds it by itself; it never formats or erases a drive.
19. **Check it:**

    ```
    aictl ark drives
    aictl ark status
    ```

    - **What you should see:** the drive as `✓ ready`, and for each content root (internal disk and drive) a colour and a percentage, for example `! 82% ready … missing: wikipedia-es-nopic (12.0 GB)`. The desktop app and the Portal's **Status** screen show the same colours.

| Colour | Means | What to do |
|---|---|---|
| **Green** | Everything is here, verified and up to date | Nothing |
| **Yellow** | Everything essential is here; something is old, a less important item is missing, orthe drive hasn't been synced for more than 30 days (`LAI-ARK-008`) | Sync it overnight on a node with internet |
| **Red** | Something essential (tier 1, a survival model, the kit or the manuals) is missing or damaged, the drive has errors, orit hasn't been synced for more than 90 days | Read the problem codes in `aictl ark status`; fix them before an emergency |

"Survival capable: yes" means: the survival brains, speech in English and Spanish, at least one essential library book and the map (with its styles) are all verified. An item that is out of date does not count until it has been updated. If the date columns show "clock wrong", the computer's clock is behind a date it has already seen: set the right date and time (`LAI-AGT-012`); until then the colours ignore age. Colour rules in detail: [ark-sync.md §4.12.2](../specs/ark-sync.md#4122-colour-rules). The cloud is stricter about drive age (amber from 21 days, alert at 30), see [08](08-scheduled-sync.md).

**Drive states you may see in `aictl ark drives`:**
- `ready`: in use.
- `not_mounted` (`LAI-ARK-005`): press **Mount** in the app, or open it in the **Files** app, or `aictl ark mount <root-id>` (Part 11). The computer never mounts a drive by itself.
- `uninitialized` (`LAI-ARK-010`): a new, empty AI-ARK drive. Part 2, step 12.
- `read_only` (`LAI-ARK-003`): it can be read and imported, not updated. Eject, plug in again.
- `unclean` (`LAI-ARK-004`): the drive has filesystem errors. Used read-only, files are re-checked before use. Fix: Part 8.
- `unusable` (`LAI-ARK-002`, `LAI-ARK-006`): not ext4, or a cloned twin. Never touched.

---

## Part 5. Safe eject

20. **Desktop:** press **Eject**. **Terminal:**

    ```
    aictl ark eject <root-id>
    ```

    Find the root id with `aictl ark drives` (the `ark-…` value). A new drive that you have not set up can be ejected too.
    - **What you should see:** the drive shows `ejecting`, and after a few seconds "You can unplug the drive now." (in Spanish: "Ya puede desconectar el disco."). Only now pull it out. If you see `ejecting` and no message yet, wait: the computer is still saving the last data to the drive. It can take a minute.
    - **If it doesn't work:** the answer is "The drive can't be ejected: files are in use" (`LAI-ARK-009`), with a list of what is using the drive. Nothing was unmounted and the drive works as before. For each line of the list:
      - **A brain** (for example "The brain *Brain X* is running from this drive"): switch to a brain stored inside the computer (`aictl load <model-id>`), then eject again. Or press **Eject anyway** in the app, or run `aictl ark eject <root-id> --force` (it asks first), which stops that brain.
      - **A program** (a name and a number, for example `bash (4242)`): it has a file of the drive open, or its terminal window is inside the drive's folder. Close the program, or in that terminal type `cd ~` and press Enter. Then eject again.
      - **sync** (the computer is still copying to the drive): wait a minute and try again, or stop it with `aictl ark sync --cancel`.

      If the app first says "ejecting" and a moment later shows `LAI-ARK-009` as a warning, the drive could not be unmounted at the last moment because something opened it: the drive is back in use, so close the listed program and press **Eject** again. **Never pull the drive while it shows `ejecting` or after this warning.**
21. **If you pulled it out without ejecting** (`LAI-ARK-007`): nothing is lost. Plug it in again once so sync can check it; next time eject first.

---

## Part 6. Storage and refresh schedule

| How often | What | How |
|---|---|---|
| **Monthly** | Refresh: plug it into a node with internet overnight | Plug in before bed; the nightly sync (03:15) updates every mounted drive. Or run `aictl ark sync --root <root-id> --wait` |
| **Every 3 months** | Full check of every file (finds silent disk damage) | `aictl ark verify --root <root-id> --full --wait` (an hour or more on a big drive) |
| **Every 3 months** | The "pull the plug" drill uses the drive too | [08 → Quarterly drill](08-scheduled-sync.md#part-6-the-quarterly-pull-the-plug-drill) |
| **About every 5 years**, or sooner on repeated errors | Replace the drive | Format a new one (Part 2), fill it (Part 3), keep the old one as a spare until the new one is green |

22. **Store it:** in a padded case, dry, cool, out of the sun, away from magnets and heaters. Put the printed [01-survival-mode.md](01-survival-mode.md) in the same case.
23. **Two drives:** refresh them on different weeks and keep them in different places.
24. **Replace it** when you see `LAI-ARK-014` (read/write errors) more than once on different ports and cables, or `LAI-SYN-025` (files no longer match their checksum) keeps coming back after `aictl ark verify --full`.

---

## Part 7. Using the drive to serve or import on any node

25. **Serve directly from the drive.** Just plug it in. Models on it become loadable (they start more slowly from a USB drive; the node waits twice as long before giving up), its books join the library, and its maps appear. Nothing is copied.
26. **Copy content into the computer** (faster and survives losing the drive). Works in every mode, with no internet:

    ```
    aictl ark import --dry-run
    aictl ark import --wait
    ```

    - **What you should see:** the plan, then copy progress, then `✓` and the internal disk's new readiness.
    - Only one item: `aictl ark import --item <id> --wait`. An item that isn't in this node's profile needs `--add-to-profile` (PIN).
    - Newer catalog on the drive (for example the node was offline for months): add `--catalog` to install it first.
    - Several drives plugged in: the command lists them with a number each. Type the number of the drive to copy from and press Enter.
    - **If it doesn't work:** `LAI-SYN-070`: a copy on the drive is damaged; that item is skipped. Sync the drive on a node with internet, or use another drive. `LAI-SYN-040`: not enough space inside; import fewer items.
27. **A computer with no Local AI software:** open **START-HERE.html** on the drive, or follow [01 Part 9](01-survival-mode.md). To build a full node from the drive with no internet: [06-rebuild-a-node-offline.md](06-rebuild-a-node-offline.md).

---

## Part 8. Repairs

28. **Filesystem errors** (`unclean`, `LAI-ARK-004`). Leave the drive plugged in. Press **Ctrl + Alt + T**, type `sudo aictl`, press **Enter**, choose **6** (*Offline content and the AI-ARK drive*), then **Repair a drive**, and pick the drive marked `has errors`. Answer `y`.
    - The typed command is `sudo aictl ark repair <root-id>` (the root id is in `aictl ark drives`).
    - **What you should see:** `Ejecting the drive (it stays plugged in)`, then lines like `Checking step 2 of 5 (folders): 40%`. It can take from minutes to over an hour on a big drive; don't unplug it. At the end: `✓ No errors were found.` or `✓ Errors were found and fixed.`, and the drive is mounted again.
    - Then run `aictl ark verify --root <root-id> --wait` (Part 13).
    - **If it doesn't work:** `LAI-ARK-009` (files in use): close the programs it names, or switch brain, and repeat. `LAI-ARK-018` (some errors are left): the drive may be failing; prepare a new drive (Part 2), fill it, and stop using this one. `LAI-ARK-017` (the check could not run): unplug the drive, plug it in again and repeat; last resort: [Appendix B](#appendix-b-repair-or-give-a-new-id-by-hand-last-resort).
29. **Cloned drive** (two drives, same id, `LAI-ARK-006`). Plug in **both** drives. In `sudo aictl`, choose **6**, then **Give a copied drive a new id**, pick the copy it offers, and answer `y`.
    - The typed command is `sudo aictl ark new-id <root-id>` (the id that `aictl ark drives` shows twice).
    - **What you should see:** `✓ The copy now has its own id, ark-…, and is mounted at …. Both drives can be used.` Nothing on the drive is deleted. `aictl ark drives` lists two different ids.
    - **If it doesn't work:** `This drive does not share its id with another drive`: only one copy is plugged in; plug in both. `LAI-ARK-017`: unplug the copy, plug it in again and repeat; last resort: [Appendix B](#appendix-b-repair-or-give-a-new-id-by-hand-last-resort).
30. **Inventory conflict** (`LAI-ARK-012`): the lab inventory changed both on the drive and on this node. Both copies are kept (the node's as `inventory.local.yaml.<node-id>.<time>.conflict` on the drive). Compare them, keep the right one, delete the other. The exact steps are in [Part 9](#part-9-start-herehtml-and-the-lab-inventory-copy).

---

## Part 9. START-HERE.html and the lab inventory copy

Every time a sync finishes on a plugged-in AI-ARK drive, the node rewrites two things at the top of the drive: **START-HERE.html** (the page anyone can open to learn what the drive is and how to use it) and a copy of the lab's **inventory** (`inventory.local.yaml`, the real addresses of the lab, so a node rebuilt from the drive alone can still print its manuals with the real values).

31. **Open the START-HERE page.** It needs no internet and no Local AI software, so you can do it on any computer. In the **Files** app open the drive and double-click **START-HERE.html**, or type (change `AI-ARK` if the drive has another name):

    ```
    xdg-open /media/$USER/AI-ARK/START-HERE.html
    ```

    - **What you should see:** a page titled **AI-ARK drive. Start here.** with the Spanish text under every English paragraph, a coloured status (**READY**, **USABLE, NEEDS ATTENTION** or **NOT READY**), the date of the last sync, and the list of manuals. Click **Open the rebuild guide**: the guide opens in the same browser.
    - **If it doesn't work:**
      - A line says **not on this drive yet** instead of a link (for example the rebuild guide or a survival card): the manuals were not rendered into the drive. Plug the drive into a node with internet and run `aictl ark sync --root <root-id> --item docs-bundle --wait`, then open the page again (it is rewritten at the end of the run).
      - The status says **UNKNOWN** and there is no table: the drive has no readiness report yet. Run `aictl ark sync --root <root-id> --wait` once.
      - There is no **START-HERE.html** at all: the drive was never synced, or it was plugged in read-only. Run `aictl ark status`; a drive with filesystem errors shows `LAI-ARK-004`, see Part 8.
      - Links do nothing in your browser: open the file from the **Files** app instead of from inside another program; the page uses only plain links between files on the drive.
32. **Is the page up to date?** At the bottom it says `this copy: <date> UTC`. If that date is old, plug the drive in and run `aictl ark sync --root <root-id> --wait`.
33. **Check the inventory copy.** It is only there if the node has its own inventory (`/etc/localai/inventory.local.yaml`) and `sync.ark_drive.copy_inventory` is `true` (the default; [08 → Part 4](08-scheduled-sync.md#part-4-run-a-sync-now-and-limit-bandwidth) shows how to change a sync setting). Look at it:

    ```
    ls -l /media/$USER/AI-ARK/inventory.local.yaml
    ```

    - **What you should see:** one line starting with `-rw-------` and `root`. Only the administrator can read it. **Never email, paste or photograph it**: it holds the lab's real addresses.
    - **If it doesn't work:** `No such file`: the node has no inventory file, or `copy_inventory` is `false`, or the drive has not been synced since. A file that anyone can read (`-rw-r--r--`) is wrong: `sudo chmod 600 /media/$USER/AI-ARK/inventory.local.yaml`.
34. **Fix an inventory conflict (`LAI-ARK-012`).** It happens when the inventory was edited on the drive **and** on the node since the last copy, or when the drive already had a different inventory the node never copied. Nothing was overwritten. Look for the saved copy:

    ```
    ls /media/$USER/AI-ARK/ | grep conflict
    ```

    - **What you should see:** a file like `inventory.local.yaml.ai-n3.20261102T031500Z.conflict` (it is the **node's** version; the file next to it, without `.conflict`, is the drive's).
    - **Compare the two** (`sudo` is needed; the output is secret, so don't share it):

      ```
      sudo diff /etc/localai/inventory.local.yaml /media/$USER/AI-ARK/inventory.local.yaml
      ```

    - **Keep the node's version on both sides:**

      ```
      sudo cp /etc/localai/inventory.local.yaml /media/$USER/AI-ARK/inventory.local.yaml
      ```

      **or keep the drive's version on both sides:**

      ```
      sudo aictl docs inventory import /media/$USER/AI-ARK/inventory.local.yaml
      ```

      It checks the file first and says `✓ The lab inventory was installed in /etc/localai/inventory.local.yaml …`.

    - Then delete the saved copies and sync once so the node notices the files agree:

      ```
      sudo rm /media/$USER/AI-ARK/inventory.local.yaml.*.conflict
      aictl ark sync --root <root-id> --wait
      ```

    - **What you should see:** `aictl doctor` no longer lists `LAI-ARK-012`.
    - **If it doesn't work:** `LAI-ARK-012` is still listed: the two files still differ (run the `diff` again; it must print nothing), or another drive has its own conflict (look in each drive). Unplugging the drive also clears the warning until the next sync of that drive.

30. **Inventory conflict** (`LAI-ARK-012`): the lab inventory changed both on the drive and on this node. Both copies are kept (the node's as `inventory.local.yaml.<node-id>.<time>.conflict` on the drive). Compare them, keep the right one, delete the other.

---

## Part 10. Freeing space (orphans and old versions)

Use this when a content folder (the internal disk or the drive) is getting full, or when `aictl ark sync --dry-run` lists items marked `orphan`. An **orphan** is content that is stored but that this node's profile no longer asks for. An **old version** is what an update leaves behind for 14 days in case the new one is bad.

> **Before you start**
> - You need: a terminal, and either `sudo` or the admin PIN.
> - **Safe by design:** the commands below **never delete the last good copy of anything**. If an item exists only in one place, it is kept and you are told so. There is no option to override that. To free that space, first copy the item to another drive (step 35).
> - Deleting is **permanent**. Look at the list first (step 32); nothing is deleted until you type `DELETE`.
> - Don't run it while a sync is running (`LAI-SYN-005`).

> 🛠 Verify when implemented (E3 prune and the sync engine; E8 for the desktop button, which asks for the admin PIN).

31. **See how full it is, and what sync calls an orphan:**

    ```
    aictl ark status
    aictl ark sync --dry-run
    ```

    - **What you should see:** the used and free space of each content folder, and in the sync list rows ending in `orphan`.
    - **If it doesn't work:** `LAI-SYN-010` or `LAI-SYN-011` only stop a real sync, not the list. If the list is empty or the command can't run, see [07 → Disk full](07-troubleshooting.md#disk-full).

32. **Ask what would be deleted. This changes nothing:**

    ```
    aictl ark prune --orphans --trash --dry-run
    ```

    Add `--root internal` (or `--root <root-id>` for a drive) to look at one content folder only.

    - **What you should see:** `Would delete 2 item(s) (14.3 GB):`, a table of item, version, where it is, size and why (`not in the profile` or `old version`), and last `Nothing was changed (dry run).`
    - A line such as `This is the only copy of gutenberg-en on this computer; it was kept.` is the safety rule working. That item is not in the list of things that will go.
    - **If it doesn't work:** it asks for the PIN: type it (nothing shows on the screen), or run the command with `sudo`. `Nothing to delete.` means there is nothing to free this way; go to step 35 or [07 → Disk full](07-troubleshooting.md#disk-full). `LAI-CAT-001`: the catalog is damaged, so nothing can be called an orphan; run `aictl catalog restore`, then try again (`--trash` alone still works).

33. **Delete them.** Read the list again, then run the same command without `--dry-run`:

    ```
    aictl ark prune --orphans --trash
    ```

    - **What you should see:** the same list, then `Delete 2 item(s) (14.3 GB) from this computer? This cannot be undone. Type DELETE to confirm:`. Type `DELETE` (capital letters) and press Enter. Then one `✓ Deleted … (… freed).` line per item and `Freed 14.3 GB.`
    - Anything else you type, including `delete` or `y`, deletes nothing: `Nothing was deleted.`
    - **If it doesn't work:** `LAI-SYN-005`: a sync is running; wait for it (`aictl ark status`) or stop it with `aictl ark sync --cancel`, then try again. `LAI-CLI-007`: there is no terminal to type in (for example inside a script); add `--yes` only if you have already read the list from step 32.

34. **Only the old versions** (nothing the profile still wants is touched):

    ```
    aictl ark prune --trash
    ```

    Sync already does this for you: old versions are removed after 14 days, and files that failed their checksum (kept in quarantine) after 7 days. When a download needs room, sync removes quarantine first and then the oldest old versions that are safe to remove, and tells you with `LAI-SYN-043` (information only). An old version is kept while its replacement has not been checked yet.

35. **When it says "only copy" and you still need the space.** Copy the item to another place first, then prune again:

    1. Plug in an AI-ARK drive that has room (Part 4) and fill it from this node:

       ```
       aictl ark sync --root <root-id> --wait
       ```

       Only items the drive's profile wants are copied (the default `ark-drive-full` wants everything in the catalog); a copy of this node's own file on the drive is made from the disk, not downloaded again.
    2. When it ends, the copy on the drive has just been checked. Now free the space on the internal disk, within 24 hours (a copy checked longer ago does not count):

       ```
       aictl ark prune --orphans --root internal
       ```

    - **What you should see:** the item now listed under `Would delete` for `internal`, and after you type `DELETE`, `Freed …`. A drive that is unplugged does **not** count as a copy: keep it plugged in while you prune.
    - **If it doesn't work:** the item is still listed as the only copy: the drive does not hold a checked copy yet (`aictl ark status` shows what the drive is missing), or it was checked more than a day ago; run `aictl ark verify --root <root-id> --item <item-id> --wait` and prune again.
    - To delete a model on purpose even though it is the last copy, use `aictl delete <model-id>` instead (it asks twice and needs the PIN); that is a different command with its own warnings, see [03 Part 3](03-swap-a-model.md#part-3-from-the-terminal).

---

## Part 11. Mount a drive, set it up, eject it and change its profile

Use this when the app shows **Mount**, **Set up this drive** or **Eject** next to the drive, or when you want a drive to follow a different plan than the default `ark-drive-full`. The three buttons do what the commands below do; each command asks the agent on this computer, so none of them needs the internet.

> **Before you start**
> - You need: a terminal (**Ctrl + Alt + T**) and the drive plugged in.
> - **Mount** and **Set up this drive** never erase or format anything. The computer never mounts a drive by itself: you press **Mount**.
> - Changing the profile needs the admin PIN.

> 🛠 Verify when implemented (E3 for the agent; the `aictl ark mount|init|eject` commands arrive with the CLI work; E8 for the desktop buttons).

36. **See what state the drive is in:**

    ```
    aictl ark drives
    ```

    - **What you should see:** one line per drive with its root id (`ark-…`) and its state: `ready`, `uninitialized`, `not_mounted`, `read_only`, `unclean`, `unusable` or `ejecting`.
    - **If it doesn't work:** no drive listed although it is plugged in: wait 30 seconds and look again, try another USB port, then see [07 → The ark drive is not found](07-troubleshooting.md#the-ark-drive-is-not-found).

37. **Mount it** (only if the state is `not_mounted`, `LAI-ARK-005`):

    ```
    aictl ark mount <root-id>
    ```

    - **What you should see:** a moment later the drive is listed with a folder (for example `/media/root/AI-ARK`) and the state `ready`, or `uninitialized` if it is a new drive. Any folder is fine.
    - **If it doesn't work:** if the drive is already `ready`, `read_only` or anything other than `not_mounted`, the command refuses and tells you its state: it does not need mounting. If it says it could not mount the drive (`LAI-ARK-005`) and gives a reason: `wrong fs type` means the drive is not ext4 (go back to Part 2); a message about errors or `fsck` means Part 8. Otherwise unplug the drive, wait 10 seconds, plug it in again, or open it once in the **Files** app.

38. **Set it up** (only if the state is `uninitialized`): follow Part 2, step 12 (`aictl ark init <root-id>`, or the **Set up this drive** button).

    - **What you should see:** the state changes to `ready` and a file `START-HERE.html` appears at the top of the drive.

39. **Eject it** before you unplug it: Part 5, step 20.

40. **Change the plan the drive follows** (admin PIN). The default is `ark-drive-full`, which wants everything in the catalog:

    ```
    aictl ark drives set-profile <root-id> <profile>
    ```

    Type the PIN when it asks (nothing shows on the screen).

    - **What you should see:** the root id and the new profile name. The next `aictl ark sync` fills the drive according to the new profile. Nothing is deleted when you change it; content the new plan no longer wants stays until you free space with Part 10.
    - **If it doesn't work:** "unknown profile" (`LAI-CAT-003`): the name is misspelled or this computer's catalog does not have it; check the spelling, or run `aictl catalog refresh` on a computer with internet and try again. "No catalog" (`LAI-CAT-001`): run `aictl catalog restore`. The drive must be plugged in and writable: if it is `read_only` (`LAI-ARK-003`), eject it, plug it in again and repeat. A wrong PIN is asked again; too many wrong PINs lock the command for a few minutes (`LAI-SEC-004`).

---

## Part 12. Importing from a drive, with no internet

Use this when a computer has **no internet** (or you are in offline or survival mode) and you want to put new models, books or maps on it from an AI-ARK drive that was filled somewhere else. It copies what the drive holds onto the computer's own disk, so the computer keeps working if the drive is lost. It never uses the network, in any mode.

> **Before you start**
> - You need: a terminal (**Ctrl + Alt + T**), the AI-ARK drive plugged in and showing as ready (Part 4), and free space on the computer's disk.
> - **The drive is only read.** Nothing on it is changed or deleted, even if it turns out to be damaged.
> - **Time:** minutes for a small item, up to an hour or more for a big model. You can leave it running.
> - You don't need `aictl mode online`. If the computer is in `offline` or `survival` mode, nothing changes: the import works the same.

> 🛠 Verify when implemented (Epic E3 import; the desktop button is E8).

41. **See which drive and what is on it.**

    ```
    aictl ark drives
    ```

    - **What you should see:** a line like `AI-ARK  ark-3f9c0a12  /media/<user>/AI-ARK  ext4  800 GB free of 2.0 TB  ✓ ready`. Note the `ark-…` word: it is the drive's **root id**.
    - **If it doesn't work:** `No ark drive found.` means the drive is not plugged in or not mounted: unplug it, plug it in again and wait 30 seconds; if it says `not mounted`, see Part 4. A drive marked `not shut down cleanly` cannot be imported from: its files may be damaged, so repair it first (Part 8) or use another drive.

42. **Look at the plan first. This changes nothing:**

    ```
    aictl ark import --dry-run
    ```

    - **What you should see:** a table of what would be copied (`download` or `update`, with a note like `from ark-3f9c0a12 (local copy)`), what is already here (`keep`), what the drive does not have (`skip` with `needs the network`), the size to copy, and last `Nothing was changed (dry run).`
    - Two drives plugged in? It stops with `LAI-ARK-015` and lists them. Run it again with the one you want: `aictl ark import --from ark-3f9c0a12 --dry-run`.
    - **If it doesn't work:** `LAI-ARK-001`: the computer has nowhere to store the content; run `aictl doctor`. `LAI-CAT-003`: the computer's profile name is not in its catalog; see [07-troubleshooting.md](07-troubleshooting.md).

43. **Copy everything the computer wants and the drive has:**

    ```
    aictl ark import --wait
    ```

    - **What you should see:** a line per item (`… Copying qwen3.5-9b-q4km  ████░░░░  41%  2.4/5.9 GB  380 MB/s`), then `✓ Import finished: 3 copied, 5 already here, 0 failed. Internal disk is ready.`
    - Items the drive does not hold are left alone. Items the computer already has in the same or a newer version are not copied again: an older version on the drive never replaces a newer one on the computer.
    - Each file is checked while it is copied. If the drive's copy has gone bad you see `LAI-SYN-070` for that item: **that copy is damaged**. The item is not installed, the drive is not touched, and the rest of the import carries on.
    - **If it doesn't work:** `LAI-SYN-070`: sync that drive on a computer with internet (`aictl ark sync --root <root-id>`), or use another drive. `LAI-SYN-040` or `Import finished: … 1 skipped`: not enough free space; import fewer items (step 44) or free space first (Part 10). `LAI-SYN-005`: another sync, check or import is running; wait for it (`aictl ark status`) or stop it with `aictl ark sync --cancel`. If you stop the import with **Ctrl + C**, run the same command again: it carries on where it stopped.

44. **Copy only one item** (for example the small speech-to-text model):

    ```
    aictl ark import --item whisper-small --dry-run
    aictl ark import --item whisper-small --wait
    ```

    - **What you should see:** the same as before, for that one item only.
    - **If it doesn't work:** `It is not in this computer's profile, so it was not copied.` The computer's list of wanted content does not include that item. To add it, run the same import with `--add-to-profile`:

      ```
      aictl ark import --item whisper-small --add-to-profile --wait
      ```

      This writes the item into the computer's settings file `/etc/localai/node.yaml` (under `profile_overrides`, and saves the old file as `node.yaml.bak`), so sync keeps it from now on. It needs the administrator: at the terminal on the computer it just works for people in the `localai-operators` group; from another machine it asks for the PIN (nothing shows while you type). `--add-to-profile` always needs `--item`.

45. **The drive carries a newer catalog** (the list of what exists and how big it is). If the computer was offline for months, install the drive's catalog first, in the same command:

    ```
    aictl ark import --catalog --dry-run
    aictl ark import --catalog --wait
    ```

    - **What you should see:** nothing extra when the drive's catalog is not newer. When it is newer and valid, it is installed first and the plan uses it.
    - **If it doesn't work:** `LAI-CAT-002` in the warnings: the catalog on the drive is not valid. The import goes on with the catalog the computer already has; nothing is lost. Tell the maintainer.

46. **Check the result:**

    ```
    aictl ark status
    ```

    - **What you should see:** the internal disk with a higher **ready** percentage, and nothing under `missing:` for what you imported. Models you copied can be loaded at once (`aictl models`).
    - **If it doesn't work:** an item you expected is still `missing`: look at the import's last lines for its code (step 43), or run `aictl ark import --item <id> --dry-run` to see why it was skipped.

The desktop app will offer the same import with a button (Epic E8); until then, use the terminal as above.

---

## Part 13. Checking that the stored content is still good

Disks wear out silently: a file can change one bit and still look fine. The computer re-checks its content a little at a time every night, and you can check by hand. Use this when you see `LAI-SYN-025`, before you trust an old drive, or every three months.

> **Before you start**
> - You need: a terminal. Works in every mode, with no internet. Checking only **reads**: it never changes a good file.
> - **Time:** a few minutes for one item; an hour or more for a whole big drive. Checking everything can be left running.
> - A check cannot start while another sync, check or import runs (`LAI-SYN-005`).

> 🛠 Verify when implemented (Epic E3 re-verification).

47. **Check one item:**

    ```
    aictl ark verify --item whisper-small --wait
    ```

    - **What you should see:** `✓ Check finished: 1 verified, 0 with problems.`
    - **If it doesn't work:** `LAI-SYN-025`: the stored file no longer matches its checksum. What happened is told on screen: the damaged copy was **set aside** (moved to the `quarantine` folder, not deleted) or, if an older good version was kept, **put back**. Go to step 49.

48. **Check everything now** (ignores how recently each item was checked):

    ```
    aictl ark verify --full --wait
    ```

    Add `--root internal` or `--root <root-id>` to check one place only.

    - **What you should see:** `✓ Check finished: 24 verified, 0 with problems.` and the readiness of each place.
    - Without `--full` the check looks only at items not checked for `sync.reverify_days` (30) and stops after `sync.reverify_budget_minutes` (90). If it says some items were left for time, run it again, or use `--full`.
    - **If it doesn't work:** the check is interrupted if you press **Ctrl + C**; nothing is damaged, and the item being read keeps its old "last checked" date. Run it again.

49. **A file failed: fix it.**

    1. Look at what the computer says: `aictl ark status`. The item is `missing` (or `stale` if an older version was put back) and the root is yellow or red.
    2. With internet, fetch it again: `aictl ark sync --item <id> --wait`. Without internet, import it from a good drive: `aictl ark import --item <id> --wait` (Part 12).
    3. A drive that is **read-only or not shut down cleanly** is never written to. The damaged item is only hidden from the computer until you unplug the drive; use another copy (the computer's own, or another drive).

    - **What you should see:** `✓ Sync finished` (or `Import finished`) and the item back to `verified`.
    - **If it doesn't work:** if files keep failing again and again, **the disk may be dying**. Copy everything to another drive now (step 35), and replace the old one (Part 1).

---

## Appendix A. Format a drive by hand (last resort)

Use this only when `sudo aictl ark format` cannot run (for example on a computer without Local AI). Part 2 is safer: it never offers the wrong disk.

> ## ⚠️ WARNING: THESE COMMANDS ERASE THE WHOLE DRIVE
> **Choosing the wrong device erases the wrong disk, including this computer's own disk.**
> - **Unplug every other USB disk and memory stick first.**
> - **Read the device name twice** before every command that contains it.
> - Never pick the disk that has `/` or `/boot` under MOUNTPOINTS.

A1. **Plug in only the new drive.** List the disks:

   ```
   lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS,FSTYPE,LABEL
   ```

   - **What you should see:** one disk with `TRAN` = `usb` and the size of your new drive, for example `sdb  1.8T  Portable SSD  usb`. The computer's own disk is usually `nvme0n1` or `sda`, with mount points like `/`. **Never pick that one.**
   - **Write down the name** of the USB disk, for example `sdb`. In every command below, replace `sdX` with it. Partition 1 of it is `sdX1` (for example `sdb1`).
   - **If it doesn't work:** no `usb` line: try another port or cable, wait 10 seconds, run it again.

A2. **Unmount anything the desktop opened from it:**

   ```
   sudo umount /dev/sdX?* 2>/dev/null; true
   ```

   - **What you should see:** nothing, or a password prompt (type your login password).

A3. **Erase old partition tables.** ⚠️ Check `sdX` is the USB disk from A1.

   ```
   sudo wipefs --all /dev/sdX
   ```

   - **What you should see:** lines like `/dev/sdX: 8 bytes were erased at offset …`, or nothing if the drive was blank.

A4. **Create one partition that fills the drive.** ⚠️ Same check.

   ```
   sudo parted --script /dev/sdX mklabel gpt mkpart AI-ARK ext4 1MiB 100%
   ```

   - **What you should see:** nothing (no output means success).

A5. **Format it as ext4 with the label AI-ARK.** ⚠️ Note the `1` at the end: the partition, not the whole disk.

   ```
   sudo mkfs.ext4 -L AI-ARK -m 0 -E lazy_itable_init=0,lazy_journal_init=0 /dev/sdX1
   ```

   (`-m 0` gives back the 5 % normally reserved for the system; `lazy_…=0` makes the first fill faster.)
   - **What you should see:** `Creating filesystem with … blocks` and, after a minute or two, `Writing superblocks and filesystem accounting information: done`.

A6. **Check it:**

   ```
   sudo blkid /dev/sdX1
   ```

   - **What you should see:** `LABEL="AI-ARK"` and `TYPE="ext4"`.
   - **If it doesn't work:** `TYPE="exfat"` or `"ntfs"`: A5 ran on the wrong partition. Start again from A1.

A7. **Mount it.** Unplug it and plug it back in (the desktop mounts it at `/media/<you>/AI-ARK`), or:

   ```
   udisksctl mount -b /dev/sdX1
   ```

   - **What you should see:** `Mounted /dev/sdX1 at /media/<you>/AI-ARK`.

A8. **Set it up for Local AI:** Part 2, step 12.

---

## Appendix B. Repair or give a new id by hand (last resort)

Use this only when `sudo aictl ark repair` or `new-id` (Part 8) cannot run.

B1. **Repair** (`LAI-ARK-004`). Find the drive's partition (the line with label `AI-ARK`, for example `sdb1`) and where it is mounted:

    ```
    lsblk -o NAME,SIZE,TRAN,MOUNTPOINTS,LABEL
    ```

    Close what uses the drive (switch brain if one runs from it). The repair needs the drive **unmounted**. Unmount it, then repair it (⚠️ use the AI-ARK partition from the list):

    ```
    sudo umount /dev/sdX1 2>/dev/null; true
    sudo fsck.ext4 -f /dev/sdX1
    ```

    - **What you should see:** five "Pass" lines; answer `y` if it asks to fix something. It can take many minutes on a big drive.
    - Then unplug it, plug it in again, and run `aictl ark verify --root <root-id> --wait`.

B2. **New id** (`LAI-ARK-006`). With both copies plugged in, `aictl ark drives` marks the copy with `LAI-ARK-006`; note its device (for example `/dev/sdc1`). Unmount it, check it, and give it a new id (⚠️ check the device):

    ```
    sudo umount /dev/sdX1
    sudo fsck.ext4 -f /dev/sdX1
    sudo tune2fs -U random /dev/sdX1
    ```

    Unplug it and plug it in again; it gets a new `ark-…` root id. Run `aictl ark init` (Part 2, step 12) if it shows `uninitialized`.
