# 04. The AI-ARK drive

Use this when you buy, format, fill, plug in, eject, store or refresh the portable **AI-ARK drive**, or when you want to use it to serve or copy content on any node.

> **Before you start**
> - You need: a node with internet (for the first fill), a USB 3 SSD, and a terminal (**Ctrl + Alt + T**).
> - Formatting **erases the whole drive**. Unplug every other USB disk first.
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

> ## ⚠️ WARNING: THESE COMMANDS ERASE THE WHOLE DRIVE
> **Choosing the wrong device erases the wrong disk, including this computer's own disk.**
> - **Unplug every other USB disk and memory stick first.**
> - **Read the device name twice** before every command that contains it.
> - Never pick the disk that has `/` or `/boot` under MOUNTPOINTS.
> - Don't use exFAT, NTFS or FAT32: they can't keep Linux permissions, so the rebuild kit wouldn't work.

The exact procedure is [ark-sync.md §4.17](../specs/ark-sync.md#417-formatting-a-new-ai-ark-drive). It's repeated here with explanations.

5. **Plug in only the new drive.** List the disks:

   ```
   lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS,FSTYPE,LABEL
   ```

   - **What you should see:** one disk with `TRAN` = `usb` and the size of your new drive, for example `sdb  1.8T  Portable SSD  usb`. The computer's own disk is usually `nvme0n1` or `sda`, with mount points like `/`. **Never pick that one.**
   - **Write down the name** of the USB disk, for example `sdb`. In every command below, replace `sdX` with it. Partition 1 of it is `sdX1` (for example `sdb1`).
   - **If it doesn't work:** no `usb` line: try another port or cable, wait 10 seconds, run it again.

6. **Unmount anything the desktop opened from it:**

   ```
   sudo umount /dev/sdX?* 2>/dev/null; true
   ```

   - **What you should see:** nothing, or a password prompt (type your login password).

7. **Erase old partition tables.** ⚠️ Check `sdX` is the USB disk from step 5.

   ```
   sudo wipefs --all /dev/sdX
   ```

   - **What you should see:** lines like `/dev/sdX: 8 bytes were erased at offset …`, or nothing if the drive was blank.

8. **Create one partition that fills the drive.** ⚠️ Same check.

   ```
   sudo parted --script /dev/sdX mklabel gpt mkpart AI-ARK ext4 1MiB 100%
   ```

   - **What you should see:** nothing (no output means success).

9. **Format it as ext4 with the label AI-ARK.** ⚠️ Note the `1` at the end: the partition, not the whole disk.

   ```
   sudo mkfs.ext4 -L AI-ARK -m 0 -E lazy_itable_init=0,lazy_journal_init=0 /dev/sdX1
   ```

   (`-m 0` gives back the 5 % normally reserved for the system; `lazy_…=0` makes the first fill faster.)
   - **What you should see:** `Creating filesystem with … blocks` and, after a minute or two, `Writing superblocks and filesystem accounting information: done`.

10. **Check it:**

    ```
    sudo blkid /dev/sdX1
    ```

    - **What you should see:** `LABEL="AI-ARK"` and `TYPE="ext4"`.
    - **If it doesn't work:** `TYPE="exfat"` or `"ntfs"`: step 9 ran on the wrong partition. Start again from step 5.

11. **Mount it.** Unplug it and plug it back in (the desktop mounts it at `/media/<you>/AI-ARK`), or:

    ```
    udisksctl mount -b /dev/sdX1
    ```

    - **What you should see:** `Mounted /dev/sdX1 at /media/<you>/AI-ARK`.

12. **Set it up for Local AI:**

    ```
    aictl ark drives
    aictl ark init /media/<you>/AI-ARK
    ```

    Replace `<you>` with your user name (the path printed in step 11). The desktop app does the same with the **Set up this drive** button.
    - **What you should see:** `aictl ark drives` first shows the drive as `uninitialized` (`LAI-ARK-010`). After `init` (it asks to confirm), run `aictl ark drives` again: `AI-ARK  ark-3f9c0a12  /media/<you>/AI-ARK  ext4  … free  ✓ ready`. The `ark-…` value is the drive's **root id**. Write it on the drive's label.
    - **If it doesn't work:** `unusable` with `LAI-ARK-002`: it isn't ext4, repeat from step 5. `LAI-ARK-006`: two drives have the same id (a cloned drive); see [07 → The ark drive is not found](07-troubleshooting.md#the-ark-drive-is-not-found).

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
- `not_mounted` (`LAI-ARK-005`): press **Mount** in the app, or open it in the **Files** app, or `aictl ark mount <root-id>`.
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

    - **What you should see:** "You can unplug the drive now." Now pull it out.
    - **If it doesn't work:** `LAI-ARK-009`: a brain or a program is using files on it; the message lists them. Switch to a brain stored inside the computer (`aictl load <model-id>`) or close the program, then eject again.
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
    - Several drives plugged in: add `--from <root-id>`.
    - **If it doesn't work:** `LAI-SYN-070`: a copy on the drive is damaged; that item is skipped. Sync the drive on a node with internet, or use another drive. `LAI-SYN-040`: not enough space inside; import fewer items.
27. **A computer with no Local AI software:** open **START-HERE.html** on the drive, or follow [01 Part 9](01-survival-mode.md). To build a full node from the drive with no internet: [06-rebuild-a-node-offline.md](06-rebuild-a-node-offline.md).

---

## Part 8. Repairs

28. **Filesystem errors** (`unclean`, `LAI-ARK-004`). Eject it (Part 5), unplug it, and plug it back in. Find its partition name (the line with label `AI-ARK`, for example `sdb1`):

    ```
    lsblk -o NAME,SIZE,TRAN,MOUNTPOINTS,LABEL
    ```

    The repair needs the drive **unmounted**. Unmount it, then repair it (⚠️ use the AI-ARK partition from the list):

    ```
    sudo umount /dev/sdX1 2>/dev/null; true
    sudo fsck.ext4 -f /dev/sdX1
    ```

    - **What you should see:** five "Pass" lines; answer `y` if it asks to fix something. It can take many minutes on a big drive.
    - Then unplug it, plug it in again, and run `aictl ark verify --root <root-id> --wait`.
29. **Cloned drive** (two drives, same id, `LAI-ARK-006`): unplug the original. With only the copy plugged in and **not mounted**, give it a new id (⚠️ check the device):

    ```
    sudo tune2fs -U random /dev/sdX1
    ```

    Plug it in again; it gets a new `ark-…` root id. Run `aictl ark init` again if it shows `uninitialized`.
30. **Inventory conflict** (`LAI-ARK-012`): the lab inventory changed both on the drive and on this node. Both copies are kept (the node's as `inventory.local.yaml.<node-id>.<time>.conflict` on the drive). Compare them, keep the right one, delete the other.
