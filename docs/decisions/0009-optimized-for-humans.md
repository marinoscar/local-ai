# ADR-0009: Everything is optimized for humans

- **Status:** Accepted
- **Date:** 2026-11-08
- **Deciders:** owner

## Context
Local AI exists for two jobs: running the lab, and keeping a working "ark" that a **non-expert** can operate with no internet, possibly under stress ([AGENTS.md](../../AGENTS.md)). Several earlier choices were made for whoever writes or maintains the software, not for the person who has to use it. For example, [aictl.md](../specs/aictl.md) §1 ruled out an interactive menu, so an operator had to remember command names such as `aictl ark status` and `aictl mode survival`. Because each component had its own spec, nothing stopped that from happening again.

## Decision
**Every part of Local AI is optimized for the human who uses it.** This covers the CLIs, installers, local UI, Survival Portal, desktop app, cloud app, error messages and docs. "Easy" is judged by a person who has **never used a terminal**, not by an engineer.

The rules (each one can be checked in review):

1. **Nothing to memorize.** Every common task can be done by reading the screen and choosing an option (arrow keys and Enter, or a click). A CLI run with no arguments on a terminal opens a menu. An installer asks for what it needs instead of requiring flags. Typed commands and flags still exist, as the fast path and for scripts, but they are never the only path.
2. **The common path needs no choices.** Defaults are correct for the common case. Anything the program can detect, it detects (GPU, profile, node name suggestion, mount point) and shows for confirmation.
3. **State first.** Every screen and status command starts with "is it OK?" in plain words: what works, what is wrong, and the single next thing to do.
4. **Every error helps.** It says what happened in plain language and carries a doctor code (`LAI-XXX-NNN`, [doctor-codes.md](../specs/doctor-codes.md)). It gives a one-sentence fix and tells the user what to do if the fix does not work. A stack trace or raw HTTP error is never the only output.
5. **Safe by design.** Destructive actions explain what will be lost, ask for confirmation, and need the admin PIN where the specs require it. Nothing deletes the last verified copy of anything. Undo is offered where it is possible.
6. **Long work shows progress.** Anything over about 2 seconds shows progress, what it is doing, and when possible the time left. It can be safely cancelled or resumed.
7. **Works in the worst case.** It works offline, keyboard only, on an 80×24 console, with no mouse and with no prior setup of the terminal. It works in English and Spanish.
8. **Docs lead with the human path.** Runbooks show the menu or button first and the typed command second. They keep "What you should see" and "If it doesn't work" after each step (the rules for plain-language runbooks).
9. **Tested with a human.** An operator-visible change is not done until someone follows it literally, as a first-time user, on a real node. The result is recorded in the PR.

**Priority when rules conflict:** offline safety ([ADR-0006](0006-standalone-offline.md), AGENTS.md rule 3) first, then ease for humans, then convenience for the lab or the developer. A change that makes the product harder for a non-expert, to save code or effort, is wrong.

**Exceptions (machine interfaces):** `--json` output, the agent REST API, the OpenAI-compatible endpoints, exit codes and file formats stay stable and precise for programs. These have their own contracts. Human-friendly behaviour is added beside them and never replaces them. A menu or prompt only appears on an interactive terminal, so scripts and pipes behave exactly as before.

## Consequences
- **Good:** the ark can be used by the people it is for. Fewer support calls and fewer mistakes under stress. The cloud CLI (`laictl`) and the node CLI (`aictl`) offer the same experience.
- **Cost:** more UI code to write and test (menus, prompts, progress, translations). Some dependencies are added, such as a TUI library. They must be vendored so the offline rebuild still works (ADR-0001).
- **Follow-ups:** [#425](https://github.com/marinoscar/local-ai/issues/425) (`aictl` full-screen menu). Specs that contradict this ADR are fixed when they are next touched, and any new spec must comply from the start. Reviewers use the "Human first" section of the pull request template.

## Alternatives considered
- **Keep the rule to runbook wording only (old AGENTS.md rule 4):** rejected. Good prose cannot rescue software that makes a person remember commands.
- **Leave the human experience to the desktop app alone:** rejected. The desktop app may not be installed or may not start, and the terminal and the Survival Portal must stand on their own in an emergency.
