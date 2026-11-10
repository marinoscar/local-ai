# ADR-0010: The agent keeps the node complete

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** owner

## Context
Ark sync downloads content and verifies it, and the rebuild kit holds everything a node needs to be rebuilt: kiwix-tools, pmtiles, piper, the offline apt packages, a ready-made speech-to-text build. But [rebuild-kit.md](../specs/rebuild-kit.md) §7 said: "The agent never executes anything from the kit automatically; only explicit root commands (`rebuild-*`, `kit install*`, the installer) do, and they verify first." So after a sync a person still had to know that those parts exist, read a doctor code, and type `sudo aictl kit install ...` or `sudo install-node.sh --repair`. On the real lab all three nodes showed "Essential (t1) partial" and sync "partial", and a person could not tell from the screen what to do next.

That breaks [ADR-0009](0009-optimized-for-humans.md) (nothing to memorize, the common path needs no choices, state first) and the purpose of the ark: be easy to operate by someone who is not an expert, possibly under stress. The rule had a reason: a program that runs files from a drive on its own is a risk. That reason is about unverified or unbounded execution, not about installing a file whose checksum has just been checked, through a fixed list of actions, on a computer the agent already administers as root.

## Decision
**The agent keeps the node complete by itself, safely.** This ADR replaces that one sentence of rebuild-kit.md §7 (the rest of §7 stays); the rules are [rebuild-kit.md §4.5](../specs/rebuild-kit.md#45-keeping-the-node-complete-ensure).

1. **What it does.** After every sync run that finished, 90 seconds after it starts and when the node goes online, the agent works out what the profile and `node.yaml` want (kiwix-tools, pmtiles, piper, the offline apt packages, the speech-to-text program when `whisper` is enabled) and installs what is missing or outdated, **from kit items that verify at that moment** (the file about to be installed is hashed against the kit's own record right before it is used; a bad checksum installs nothing and raises the existing `LAI-KIT-001`). It uses the install functions of `aictl kit install` and `install-debs`, which are the ones the installer's repair already uses: no new installer logic.
2. **Allowlist only.** The agent never runs anything from the kit that is not one of those named installs. The set of actions is a closed table in code (`internal/ensure/allowlist.go`), checked again right before every step; data cannot add a row. Every step is journaled (`/var/lib/localai/ensure/journal.json`), audited (`ensure.step`, `ensure.run`), idempotent (a second pass changes nothing), cancellable, and run one at a time.
3. **What needs a confirmation, never automatic.** Compiling from source (it takes hours); restarting a service that is serving (the library service after kiwix-tools was replaced); replacing the agent binary (that stays `aictl update` and the cloud button, and this engine cannot do it at all); anything destructive (nothing here deletes). A person confirms them with the button, `aictl ensure --yes`, or `POST /ensure` with `confirm` (admin*, i.e. a person at the machine or with the PIN). Restoring a **ready-made** whisper build is automatic, but only when the node has no speech program at all; replacing one that works is never automatic.
4. **One button for people.** "Install what's missing" in the Portal, the desktop app and the cloud node page, and `aictl ensure` (also in the `aictl` menu): each shows what will be done, the progress, the result, and can cancel. The doctor fixes that said "run `sudo aictl kit install ...`" lead with it; the typed command stays as the second path.
5. **A setting.** `ensure.auto` in `node.yaml`: `on` (default), `ask` or `off`. With `ask` or `off` the agent only reports (doctor code `LAI-KIT-052`) and waits for the button or the command.
6. **The state shown to people** is "Complete", "Installing what's missing (n of m)" or "Needs you: <one thing>", before any tier jargon.
7. **Offline is the normal case.** The pass uses only what is on disk. It runs in `online`, `offline` and `survival`; it waits while a sync run holds the run slot or a mode change is in flight.

## Consequences
- **Good:** a node becomes complete after a sync without a typed command; the one thing a person must do is said in plain words; failures have a doctor code (`LAI-KIT-051`) and a one-sentence fix in English and Spanish; the same engine serves the CLI, the Portal, the desktop and the cloud.
- **Cost:** the agent now changes `/opt/localai` and the package database on its own. That is bounded by the allowlist, the verification, the journal and the audit log, and switched off by one setting. The tests prove it with fakes for every action: nothing runs when its kit item did not verify, a flipped byte installs nothing, an action not in the table is refused.
- **Not changed:** the agent is still never updated by this (explicit `aictl update`), llama.cpp is never compiled automatically, and nothing is deleted.
- **Follow-ups:** the buttons (Portal, desktop, cloud node page) and the `ensure` block of the cloud state snapshot, in the next pull request of issue #480.

## Alternatives considered
- **Keep the rule and only improve the doctor texts:** rejected. A person would still have to type commands; the lab nodes show that the information was already there and still did not get used.
- **Run the installer's `--repair` from the agent:** rejected. It re-runs fifteen phases, including ones that change identity-adjacent files and need the installer's own privileges and prompts; the ensure engine reuses only the idempotent install steps.
- **Install automatically without a setting:** rejected. A deployment that wants to see every change first needs `ask` or `off`.
- **Also compile automatically when no ready-made build exists:** rejected. A compile takes up to an hour of full processor use, on a laptop that may be on battery or serving; a person decides.
