LOCAL AI REBUILD KIT
====================

This folder lets you rebuild a Local AI computer with NO internet.
It holds the programs, the source code, the Ubuntu packages, the graphics
driver packages and the catalog that a rebuild needs. An Ubuntu install
USB stick is the only other thing you need.

Do not edit, rename or delete anything in this folder by hand. The Local
AI agent keeps it up to date and checks it. This file is rewritten after
every change.

Written (UTC):  2026-11-02 18:04
Catalog:        2026.11.02-1
Items here:     6 (115 B)

WHERE TO START
--------------
1. Read the guide "Rebuild a node offline". Open this file in any web browser:
       ../docs/06-rebuild-a-node-offline.html
   All the manuals are listed in ../docs/index.html.
2. To see what is here:             aictl kit status
3. To check nothing is damaged:     aictl kit verify
4. To rebuild a computer: install Ubuntu from the USB stick, then run
       installers/install-node.sh
   from this folder, as the guide says, step by step.

WHAT IS IN EACH FOLDER
----------------------
catalog/     The catalog this kit was planned with, and pins.json.
             here (1 item, 22 B)
installers/  install-node.sh, the installer that starts an offline rebuild.
             here, with the agent program
bin/         The Local AI agent program for both CPU types, with signatures.
             here (1 item, 33 B)
src/         Source code (llama.cpp, whisper.cpp, agent), ready to build.
             here (1 item, 14 B)
git/         Full copies of the source repositories, to change version.
             here (1 item, 27 B)
toolchains/  The Go compiler, to rebuild the agent with no internet.
             here (1 item, 8 B)
tools/       kiwix-tools (Wikipedia reader), pmtiles (maps), piper (voice).
             NOT HERE YET
desktop/     The ai-desktop app installers.
             NOT HERE YET
debs/        Ubuntu packages (compilers, Vulkan, tools), per Ubuntu release.
             here (1 item, 11 B)
nvidia/      NVIDIA graphics driver packages, one folder per driver version.
             NOT HERE YET
cuda/        Optional smaller CUDA compiler files.
             NOT HERE YET
prebuilt/    Ready-made llama.cpp and whisper.cpp builds from the computers.
             none yet (a computer adds its own after it builds)
iso/         The Ubuntu install image, to make a new install USB stick.
             not here (only the AI-ARK drive keeps this)
config/      Backups of each computer's settings. Never deleted by the agent.

NEEDS ATTENTION
---------------
Missing: kit-desktop, kit-kiwix-tools, kit-nvidia-resolute-amd64-595-open,
   kit-piper, kit-pmtiles, kit-whisper-cpp
   Connect to the internet and run: aictl ark sync

The same index in machine-readable form is MANIFEST.json, next to this file.
