# 00. Start here

> **Before you start:** nothing. Just read this page. **Time:** 3 minutes.

## What this is
Each computer in this lab is an **AI node**: a normal Ubuntu computer with an AI that runs **on the computer itself**, not on the internet. Each one also carries an offline **library** (Wikipedia, medicine, how-to and repair guides), **maps**, **voice** (it can listen and talk), its own **manuals**, and a **kit** to repair itself without internet.

When the internet works, all the nodes are managed together from one website, `https://ai.marin.cr`. When the internet disappears, **every node keeps working on its own.** Nothing needs to be fixed first.

The **AI-ARK drive** is a portable disk with a copy of everything: AI models, library, maps, manuals and the repair kit. With it, any computer can become a node again.

## The three modes, in plain words
| Mode | What it means | When to use it |
|---|---|---|
| **Online** | Normal. The computer is part of the lab, can be reached from the internet with a key, and updates itself at night. | Every day |
| **Offline** | Private. Nothing goes in or out. The AI on this computer still works. | Travel, privacy, testing "what if the internet dies" |
| **Survival** | Offline, **plus** everything you need in an emergency is started, and a big-button screen (the **Survival Portal**) opens. | Emergency. Anyone can switch to it, no PIN needed |

The mode is remembered after a restart. Leaving Survival mode needs the **admin PIN** (a 4 to 8 digit number chosen when the computer was set up).

## Which page do I open?
| Your situation | Open |
|---|---|
| **No internet?** Emergency? | [01-survival-mode.md](01-survival-mode.md) (print it!) |
| Want **one page to keep next to the computer**? | The Survival card, [00-survival-card.md](00-survival-card.md) (print the filled-in copy from the Portal: **6 Manuals → Print the Survival card**) |
| **Want to chat** with the AI? | Survival mode: [01](01-survival-mode.md), button **1 Chat**. Normal day: [02-chat-and-api-keys.md](02-chat-and-api-keys.md) |
| **Need the API key** for a program? | [02-chat-and-api-keys.md](02-chat-and-api-keys.md) |
| Want a different or bigger **AI model**? | [03-swap-a-model.md](03-swap-a-model.md) |
| Bought a new **AI-ARK drive**, or plugging one in? | [04-ark-drive.md](04-ark-drive.md) |
| **Add a machine** to the lab? | [05-add-a-node.md](05-add-a-node.md) |
| Computer **wiped or dead**, and no internet? | [06-rebuild-a-node-offline.md](06-rebuild-a-node-offline.md) |
| **Something broke**, or you see a code like `LAI-SVC-001`? | [07-troubleshooting.md](07-troubleshooting.md) |
| Is the ark **green**? What runs at night? | [08-scheduled-sync.md](08-scheduled-sync.md) |
| A **laptop** must work as a server (lid closed)? | [09-laptop-as-server.md](09-laptop-as-server.md) |
| You run the **whole lab**? | [10-lab-operations.md](10-lab-operations.md) |
| Want to **make a song**, or switch a computer between **Coding** and **Music**? | [12-make-music-and-switch-workloads.md](12-make-music-and-switch-workloads.md) |

## Three things to remember

<!-- runbook-lint: no-expect-section (a short list of reminders, not a procedure) -->
1. **The first thing to try is always the doctor.** Open a terminal (**Ctrl + Alt + T**), type `aictl`, press **Enter**. A menu opens: press **2** (Find and fix problems) and do what it says. Every problem comes with a code and a one-line fix. Everything else is in the same menu, so you do not need to remember any command (typed in one line: `aictl doctor`).
2. **If the app doesn't open,** any web browser works: go to `http://127.0.0.1:7070`.
3. **Plug it in.** A laptop on battery stops when the battery is empty.
