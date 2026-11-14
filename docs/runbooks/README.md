# Runbooks: how to run Local AI, step by step

These pages are for **people**, not programmers. Each one tells you exactly what to press or type, what you should see, and what to do if it doesn't work.

**New here?** Start with [00-START-HERE.md](00-START-HERE.md).
**Emergency, no internet?** Go straight to [01-survival-mode.md](01-survival-mode.md). Print it and keep it with the computer, together with the one-page [Survival card](00-survival-card.md).

| Runbook | Use this when… |
|---|---|
| [00-START-HERE.md](00-START-HERE.md) | You don't know where to begin, or you want to know what this system is |
| [00-survival-card.md](00-survival-card.md) | You want the **one-page** card to keep next to the computer. It is a template: print the filled-in copy for each computer (Portal **6 Manuals → Print the Survival card**, or `sudo aictl docs render`). **English and Spanish** |
| [01-survival-mode.md](01-survival-mode.md) | There is **no internet** and you need the AI, the library, maps or first aid. **Printable, English and Spanish** |
| [02-chat-and-api-keys.md](02-chat-and-api-keys.md) | You want to chat, or connect a program (OpenCode, Open WebUI, a script) to a node or to the whole lab |
| [03-swap-a-model.md](03-swap-a-model.md) | You want a different AI model ("brain") on a computer, or you want to add a new model to the catalog |
| [04-ark-drive.md](04-ark-drive.md) | You are buying, formatting, filling, plugging in, ejecting or storing the portable AI-ARK drive |
| [05-add-a-node.md](05-add-a-node.md) | You are adding a new computer to the lab, publishing it on the internet, or migrating an old node (ai-n1, ai-n2, ai-n3) |
| [06-rebuild-a-node-offline.md](06-rebuild-a-node-offline.md) | A computer is new, wiped or broken, there is **no internet**, and you have an Ubuntu USB stick and an AI-ARK drive |
| [07-troubleshooting.md](07-troubleshooting.md) | Something doesn't work, or you see a code like `LAI-SVC-001` |
| [08-scheduled-sync.md](08-scheduled-sync.md) | You want to know what runs at night, whether the ark is green, or how to read the monthly self-test |
| [09-laptop-as-server.md](09-laptop-as-server.md) | A laptop must stay on with the lid closed, never sleep, and stay reachable |
| [10-lab-operations.md](10-lab-operations.md) | You run the whole lab from `https://ai.marin.cr`: weekly and monthly checks, updates, keys, alerts, and installing or updating the cloud app itself (Part 9) |
| [11-gateway-load-test.md](11-gateway-load-test.md) | You want to prove the gateway copes with 200 streams at once (fake test), or load-test the real lab off-hours |
| [12-make-music-and-switch-workloads.md](12-make-music-and-switch-workloads.md) | You want a computer to **make a song** from your lyrics (with an optional sample), or to switch what it does (**Coding**, **Music**, ...). **English and Spanish** |

## How these pages are written
- Steps are numbered. Commands go in grey boxes: type them exactly, then press **Enter**. Buttons are in **bold**.
- After each step: **What you should see**, then **If it doesn't work**, with a code and a link to [07-troubleshooting.md](07-troubleshooting.md).
- When a page needs a terminal: press **Ctrl + Alt + T** on the keyboard. A black or purple window opens. That is the terminal.
- `sudo` in front of a command means "do this as the administrator". The computer asks for **your login password**. Nothing appears on screen while you type it; that's normal. Press **Enter** when done.
- Values in angle brackets, like `<VPS_PUBLIC_IP>` or `<root-id>`, are **placeholders**. Replace them, including the `<` and `>`, with the real value. Real lab values are in `inventory.local.yaml` on the ark drive. Printed copies made with `aictl docs render` already have them filled in.
- `> 🛠 Verify when implemented (Epic E<n>).` marks a part that describes software that isn't finished yet. The steps follow the design; check them on a real node when that epic ships and fix the page if anything differs.

## How these pages are kept true
`scripts/check-runbooks.sh` runs in CI (job `runbooks` of `docs-guard`) and before you push. It builds `aictl` from `agent/` and fails when a page shows an `aictl` or `aiagent` command or flag that the real program does not have, a `LAI-…` code that is not in [specs/doctor-codes.md](../specs/doctor-codes.md) (or a leftover placeholder code), steps that are not numbered 1, 2, 3 …, an installed path that is not in [architecture/overview.md §3](../architecture/overview.md#3-canonical-names-single-source-of-truth), a rendered manual below a sub-folder (they are flat: `<root>/docs/01-survival-mode.html`), a sentence glued to the next one, or a numbered step without **What you should see** and **If it doesn't work**.
- A step list that has nothing to show (buying a disk, a checklist) says so with a hidden line, `<!-- runbook-lint: no-expect-section (why) -->` under its heading, or `<!-- runbook-lint: no-expect -->` inside one step.
- Step lists written before the check existed are in `scripts/check-runbooks.baseline`. A new step list must have its answers; an entry goes away (the check insists) as soon as its step list has them. Writing them while following the page literally on a node is what the verification issues (#166 to #176) ask for.
- This check proves that the page and the program agree. It does **not** prove the page works on a real computer: only following it on a node does, and then the `🛠 Verify when implemented` marker goes and the `> Verified on <node-id>, <date>, agent <version>` line is added.

## Where the technical details live
These pages show commands; they don't define them. If a page and a spec disagree, the spec wins and the page must be fixed:
- Commands: [specs/aictl.md](../specs/aictl.md)
- Names, paths, ports, units: [architecture/overview.md §3](../architecture/overview.md#3-canonical-names-single-source-of-truth)
- Modes: [architecture/modes.md](../architecture/modes.md)
- Error codes: [specs/doctor-codes.md](../specs/doctor-codes.md)
