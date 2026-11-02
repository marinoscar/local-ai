> **Imported source (archive, redacted).** Original: https://gist.github.com/marinoscar/8c39bd470eb5d29cb43771d6f58b2454 (ms-s1-max-local-ai-llamacpp-runbook.md)  
> Imported: 2026-11-02. Lab-specific values replaced with placeholders (see `inventory.example.yaml`).  
> This file is a historical record only. The maintained instructions are in [`docs/runbooks/`](../runbooks/) and [`docs/specs/`](../specs/).

# MINISFORUM MS-S1 Max — Local AI / llama.cpp Runbook

**Recorded:** October 12, 2026  
**Last updated:** October 13, 2026 (v3.3: long-context scaling research, benchmarks, and safe optimization plan)  
**Runbook version:** 3.3  
**Purpose:** Reproduce the GPU-accelerated local LLM setup, preserve tested commands and measured results, and document what we learned.  
**Scope:** AI host hardware specification, recorded OS/kernel/driver versions, GPU stack, model downloads, llama.cpp Vulkan/MTP, verified 64K context and proposed 96K/128K expansion, systemd/Cockpit, authenticated chat UI and API, OpenCode integration, real-task benchmarks, telemetry, and future optimization. **Excludes OS installation and dual-boot setup.**

> **Evidence key:** **Verified** means we saw a successful command/result in this session. **Configured / instructed** means we provided a command or procedure but did not receive conclusive output of its execution. **Not yet verified** means the result remains unknown. Do not treat a suggested optimization as a proven cause of a benchmark improvement.

## 1. Working configuration at a glance

| Item | Configuration / observation | Status |
| --- | --- | --- |
| Computer | MINISFORUM MS-S1 Max | Verified |
| Processor | AMD Ryzen AI Max+ 395 | Verified by `rocminfo` |
| GPU | Radeon 8060S, AMD `gfx1151` / Strix Halo | Verified by `rocminfo` and `vulkaninfo` |
| Installed memory | 128 GB unified memory; Linux reported ~121 GiB usable, ~118 GiB available at initial check | Verified |
| OS | Ubuntu 26.04.1 LTS, kernel `7.0.0-38-generic` at time of setup | Verified |
| Vulkan | RADV, Mesa `26.0.8-1ubuntu0.3`; Vulkan API `1.4.335` on the AMD GPU | Verified |
| Compute device files | `/dev/kfd`, `/dev/dri/renderD128` | Verified present |
| Dedicated GPU UMA allocation | ~2 GiB | Verified via sysfs |
| GPU GTT reported limit | ~61 GiB | Verified via sysfs |
| User | `marinoscar`; subsequently added to `render` and `video` groups | Verified |
| Download tool | Hugging Face `hf` CLI `2.2.0` installed through `pipx` | Verified |
| Model | `ggml-org/Qwen3.8-27B-GGUF`, `Qwen3.8-27B-Q4_K_M.gguf` | Verified downloaded |
| Model path | `/home/marinoscar/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf` | Verified |
| Model download size | 19.0 GB reported by downloader; `ls -lh` displayed 18G (different units/rounding) | Verified |
| Inference engine | Local source build of `ggml-org/llama.cpp` with Vulkan enabled | Verified operational |
| Server | `llama-server`, OpenAI-compatible endpoint at `http://127.0.0.1:8080` | Verified |
| API security | `LoadCredential` + `--api-key-file` | **Verified: HTTP 401 without key, HTTP 200 with key** |
| API-key storage | `/etc/llama-qwen/api-key`; OpenCode copy at `~/.config/opencode/llama-api-key` | Configured, actual key excluded from document |
| Context setting | **`-c 65536` (64K)**; initial test used `-c 8192`; 96K and 128K are research/planned only | **Verified via `/props`: `n_ctx = 65536`** |
| GPU offload request | `-ngl 99` | Used in documented launch command; exact offloaded layers not independently captured |
| Reasoning | Enabled and returning `reasoning_content` plus a final answer | Verified |
| Baseline generation | **11.27–11.28 tokens/s** | Verified |
| Best recorded generation | **24.15 tokens/s** | Verified |
| MTP engine flag support | `--spec-type draft-mtp` available in compiled llama.cpp | Verified via `--help` |
| MTP draft GGUF | `mtp-Qwen3.8-27B-Q4_0.gguf` (~1.6G as `ls -lh` reports) | Verified present |
| MTP initialization | Separate draft model loaded by llama.cpp under systemd | **Verified in journal** |
| MTP acceptance (one observed request) | **30 / 46 = 65.217%**, mean draft length **2.30** | **Verified in journal** |
| Systemd-backed Web UI generation | **21.18 tok/s** (earlier server log); **22.43 tok/s** (later UI with 64K setting) | Verified (different prompts/methods) |
| Persistent AI service | `llama-qwen.service`, systemd, running as `marinoscar` | Verified active and model loaded |
| Boot behavior | Previously verified **disabled** for auto-start; `systemctl enable --now` is now the intended configuration | **Command provided; activation after reboot not yet verified** |
| Administration UI | Linux **Cockpit** on HTTPS port **9090** | Verified accessible |
| Chat UI | Built-in llama.cpp Web UI at `http://127.0.0.1:8080` | **Verified with API-key login** |
| OpenCode | Coding agent configured against `local-llama/qwen3.8-27b` through local OpenAI-compatible API | Verified by OpenCode Build-mode UI, tool calls and generated file |
| OpenCode test | iPhone-style HTML calculator; one session, 14 messages, 16 tool calls | Verified from screenshot and `opencode stats` |
| OpenCode inference performance | **14 completed requests; 9,206 generated tokens; weighted 22.30 tok/s; 76.45% aggregate MTP acceptance** | Derived from the user's supplied `journalctl` logs, window 20:40–20:48 CDT |
| GPU utilization | **~98%** observed while OpenCode ran | User-reported monitoring observation; sensor trace not preserved |
| OpenCode task duration | **~7m38s of summed server prompt+generation time**; end-to-end wall time not captured | Calculated from 14 logged requests; not a full-task stopwatch |

**Key takeaway:** We now have a **working, browser-managed Qwen3.8-27B Vulkan/MTP inference server connected to OpenCode**, which completed a real file-editing coding task. The journal explicitly confirms draft-model initialization and speculative token acceptance. A reasoning-enabled API run reached **24.15 tok/s**; the later Cockpit-managed service delivered **21.18 tok/s** on a short Web UI request. These measurements demonstrate functioning MTP, but **do not by themselves prove that MTP alone caused the entire 2.14× observed speed difference**—a controlled on/off benchmark is still outstanding.

## 1A. Host platform and OS — detailed reproducibility baseline

> **Why preserve this:** Model throughput and compatibility are sensitive to the **CPU/APU SKU, memory capacity and bandwidth, UMA/GTT policy, GPU architecture, Linux kernel, Mesa/RADV, firmware, power profile and inference-engine revision**. A benchmark without this baseline is difficult to reproduce or compare after updates. This section records the **October 12, 2026** environment; the automated inventory command below is for future refreshes. This section **does not cover OS installation, disk partitioning or dual-boot procedures**.

### 1A.1 Confirmed host inventory — observed on this machine

| Layer | Observed value | Evidence / limitations |
| --- | --- | --- |
| Machine | **MINISFORUM MS-S1 Max** | Cockpit's hardware/model panel showed `Micro Computer (HK) Tech Limited MS-S1 MAX`. |
| Hostname / login | **`AI-MARIN-LINUX`**, account **`marinoscar`** | Shell prompts and Cockpit. |
| CPU/APU | **AMD Ryzen AI Max+ 395**, Strix Halo | Reported by `rocminfo` and `lspci`. |
| CPU logical threads | **32 CPUs** | Cockpit reported `0% of 32 CPUs`; architectural core count below is manufacturer specification. |
| GPU | **AMD Radeon 8060S**, device **`1002:1586`**, **`gfx1151`** | `lspci`, `rocminfo`, `vulkaninfo`. This is an integrated GPU sharing system RAM. |
| NPU presence | **`aie2` / `AIE-ML`** device enumerated | `rocminfo` output; **not used or benchmarked** by the Vulkan llama.cpp workload. |
| Installed RAM | **128 GB unified-memory configuration** | User's system configuration; Linux reported **121 GiB total**, about **118 GiB available** on first snapshot. |
| Swap | **8.0 GiB** | `free -h`; not a substitute for RAM for fast model inference. |
| Dedicated graphics memory reported | **2.0 GiB** | `/sys/class/drm/card1/device/mem_info_vram_total`. |
| GTT memory reported | **61 GiB** | `/sys/class/drm/card1/device/mem_info_gtt_total`; not an additional 61 GiB physical GPU chip. |
| Main SSD | **Kingston OM8TAP42048K1-A00**, nominal **2 TB**, exposed as **`nvme0n1`** | Kingston model appeared in the UEFI device listing and Ubuntu installer; Linux installer listed ~2.05 TB decimal. Exact SSD firmware and PCIe negotiated link speed were **not recorded**. |
| OS family / flavor | **Ubuntu 26.04.1 LTS Desktop**, **Resolute Raccoon**, x86-64 / amd64 | Cockpit showed Ubuntu 26.04.1 LTS, installation used amd64; Resolute is Canonical's release codename. |
| OS kernel | **`7.0.0-38-generic`** | Actual `uname -r` output during initial AI setup; could change after an OS update. |
| GPU kernel interfaces | **`/dev/kfd`**, **`/dev/dri/renderD128`** | Both present. User was added to **`render`** and **`video`** groups and confirmed a fresh session included them. |
| GPU Vulkan ICD | **Mesa RADV**, `DRIVER_ID_MESA_RADV` | `vulkaninfo --summary`. |
| Mesa Vulkan driver | **`26.0.8-1ubuntu0.3`** | `vulkaninfo --summary` reported `driverInfo`; separate reported `driverVersion` = 26.0.8. |
| Vulkan API on Radeon | **1.4.335** | GPU0 reported `apiVersion = 1.4.335`; instance version was `1.4.341`. |
| Software Vulkan alternative | **llvmpipe** (CPU renderer) | Enumerated as GPU1; **not** evidence that GPU inference fell back to CPU. |
| ROCm probe | `rocminfo` enumerated **`gfx1151`** | Shows recognition of compute devices; no standalone ROCm llama.cpp inference benchmark or complete ROCm component version captured. |
| Python / HF tooling | **Python 3.14.4**, **`hf` 2.2.0**, **`pipx` 1.8.0-1** | Recorded during successful Hugging Face CLI setup; values are time-specific. |
| Local software locations | `~/llama.cpp`, `~/models/qwen3.8-27b`, `~/.config/opencode/` | Verified host paths in shell and systemd configuration. |
| AI service / admin UI | `llama-qwen.service` at `127.0.0.1:8080`; Cockpit at HTTPS port `9090` | Service and UIs tested. See Sections 10–12 and 19 for management/authentication. |

**Clarifying the memory units:** 128 GB is the product's advertised decimal RAM size; `free -h` shows usable memory in **GiB** after hardware/firmware reservations. **2 GiB UMA + 61 GiB GTT is not 63 GiB of separate dedicated VRAM**; the GPU accesses shared system memory. Do not equate these values with maximum model size or maximum safe context window.

### 1A.2 Manufacturer hardware specification — published, not independently measured

These are useful for capacity planning, but are **specifications for the model family, not proof of the active clock, power, network speed or firmware mode on this particular host**.

| Component | Published capability |
| --- | --- |
| CPU | AMD **Zen 5**, **16 cores / 32 threads**, base 3.0 GHz, boost **up to 5.1 GHz** |
| GPU | Radeon 8060S, **RDNA 3.5**, **40 compute units**, GPU clock up to **2.9 GHz** |
| NPU | AMD XDNA 2-class accelerator, advertised **up to 50 TOPS**; this is **not** llama.cpp Vulkan tokens/sec |
| Unified memory | LPDDR5X **up to 8000 MT/s**, **256-bit** memory interface; theoretical peak bandwidth ~**256 GB/s**; actual operating rate not captured |
| Installed configuration | MINISFORUM 128 GB RAM / nominal 2 TB NVMe SSD variant |
| Wired networking | **2 × 10GbE RJ45** (Realtek RTL8127 per vendor); neither NIC speed was measured in this session |
| Wireless | **Wi-Fi 7** and Bluetooth 5.4 per MINISFORUM; not measured |
| Storage expansion | Two M.2 2280 positions with different PCIe lane allocations (vendor specifies one PCIe 4.0 ×4 and one PCIe 4.0 ×1); installed-slot identity not verified |
| Other I/O | USB4 and USB4 v2, HDMI, USB-A, PCIe expansion; refer to exact vendor SKU for port map |
| Thermal and power design | Six heat pipes, phase-change thermal material, dual fans; advertised configurable profiles **Quiet ~60W / Balanced ~95W / Performance ~130W**. **Actual selected BIOS profile, sustained chip power and temperatures were not recorded.** |

**Primary specification sources (review before reusing these figures on another SKU):**

- [MINISFORUM MS-S1 Max 128GB product / specifications](https://store.minisforum.com/products/minisforum-ms-s1-max-mini-pc)
- [AMD Ryzen AI Max+ 395 processor specification](https://www.amd.com/en/products/processors/laptop/ryzen/ai-300-series/amd-ryzen-ai-max-plus-395.html)
- [AMD ROCm GPU reference: Radeon 8060S / gfx1151](https://rocm.docs.amd.com/en/latest/reference/gpu-specs.html)
- [Canonical Ubuntu 26.04 LTS release information](https://canonical.com/blog/canonical-releases-ubuntu-26-04-lts-resolute-raccoon)

### 1A.3 Versions and operating conditions not yet captured

A future optimization run should record the following rather than guessing:

- **UEFI/BIOS vendor, version and release date**; BIOS performance/power mode, UMA frame-buffer policy and any manually configured GTT/TTM kernel settings.
- **Actual RAM data rate**, if the hardware exposes it. **8000 MT/s is a specification, not a measured result.**
- **Full kernel-package versions**, `amdgpu` module information, Mesa/RADV packages and any ROCm runtime packages actually installed.
- **llama.cpp exact Git commit/build ID**, OpenCode version, GGUF SHA-256 checksums, model runtime flags, and driver/device selection. Previous sections contain the commands to capture these.
- **CPU/GPU temperature, clocks, power and thermal throttling under sustained inference**, plus machine-selected performance mode.
- **Network interface IDs, negotiated link speeds**, and SSD negotiated PCIe speed/firmware if relevant to model startup or remote-serving latency. Keep MACs, serial numbers, personal addresses, credentials, and tokens out of shared reports.

### 1A.4 Repeatable host inventory capture (run before every comparative benchmark)

This command writes a text snapshot **without exporting API keys or the OpenCode credential file**. It will probe host information and a few read-only tools. Review the output for sensitive machine identifiers before sharing it externally.

```bash
OUT="$HOME/ai-host-inventory-$(date -u +%Y%m%dT%H%M%SZ).txt"
(
  echo '=== Captured UTC ==='; date -u --iso-8601=seconds
  echo '=== OS ==='; grep -E '^(PRETTY_NAME|VERSION_ID|VERSION_CODENAME|ID)=' /etc/os-release
  echo '=== Kernel/architecture/host ==='; uname -r; uname -m; hostname
  echo '=== CPU ==='; lscpu | grep -E 'Model name:|Architecture:|CPU\(s\):|Thread\(s\) per core:|Core\(s\) per socket:|Socket\(s\):' || true
  echo '=== RAM and swap ==='; free -h; swapon --show
  echo '=== PCI graphics/network devices ==='; lspci -nn | grep -Ei 'display|vga|3d|ethernet|network' || true
  echo '=== GPU compute ==='; rocminfo | grep -E 'gfx1151|Marketing Name:|aie2' | head -16 || true
  echo '=== Vulkan GPU/driver ==='; vulkaninfo --summary 2>&1 | grep -E 'apiVersion|deviceName|driverName|driverInfo' || true
  echo '=== AMD GPU memory exposed ==='
  for f in /sys/class/drm/card*/device/mem_info_{vram,gtt}_total; do
    if [ -r "$f" ]; then printf '%s: ' "$f"; numfmt --to=iec < "$f"; fi
  done
  echo '=== Disk devices (no serial numbers) ==='; lsblk -d -o NAME,MODEL,SIZE,ROTA,TRAN
  echo '=== BIOS version/date (no system serial) ==='
  sudo dmidecode -s bios-version; sudo dmidecode -s bios-release-date
  echo '=== Selected distro package versions ==='
  dpkg-query -W -f='${Package} ${Version}
' mesa-vulkan-drivers libvulkan1 rocminfo vulkan-tools pipx cockpit 2>/dev/null || true
  echo '=== Tools ==='
  hf --version 2>&1 || true
  opencode --version 2>&1 || true
  git -C "$HOME/llama.cpp" rev-parse HEAD 2>&1 || true
  echo '=== Service state (no secrets) ==='
  systemctl is-enabled llama-qwen.service 2>&1 || true
  systemctl is-active llama-qwen.service 2>&1 || true
) > "$OUT" 2>&1
chmod 600 "$OUT"
printf 'Inventory saved: %s
' "$OUT"
```

For future benchmark records, **store the inventory beside the matching OpenCode exported session, bounded llama.cpp journal, exact prompt, run start/stop times, and measured power/temperature logs**. The inventory is a command to **run on the user's computer**; none of its uncaptured results should be described as already verified. Do not save or export `/etc/llama-qwen/api-key` or `~/.config/opencode/llama-api-key`.

### Changes in version 3.2 (hardware and OS inventory)

- Expanded the brief hardware line items into **actual observed host data** and separately labeled **vendor specifications**.
- Added the Ubuntu release/kernel, AMD APU/GPU/NPU, Vulkan and Mesa version, unified-memory behavior, nominal SSD model, platform connectivity, thermal/power capability, and explicitly unknown BIOS/runtime versions.
- Added a **reusable environment-inventory command** to capture the host state before future model or inference-engine benchmark comparisons, without collecting API secrets.
- Preserved all existing llama.cpp, systemd, API security, OpenCode, and benchmark sections. **Still excludes OS installation and dual boot.**

### Changes in version 3.1 (API-key protection)

- Added **Section 19** with exact key-generation commands, file permissions, systemd credential injection, tested 401/200 authorization, OpenCode file-based key reference, browser UI authentication, service restart/boot behavior and key rotation guidance.
- Updated the existing systemd and OpenCode configuration templates to the **verified protected configuration**.
- **Verified:** native llama.cpp authorization is enforced on chat-completions; OpenCode and the Web UI both work with the key. **Not yet recorded:** successful post-reboot check and `systemctl is-enabled` output.

### Changes in version 3.0 (OpenCode / coding-agent baseline)

- Added the exact **OpenCode installation command**, a reconstructable `~/.config/opencode/opencode.json` provider example, model/context/output limits and localhost versus remote/Tailscale tunneling.
- Documented the **exact calculator test prompt**, test directory and HTML artifact, observable result, OpenCode's model/tool/session statistics, and the limits of what the screenshots prove.
- Preserved the raw **`opencode stats`, session listing/export and `journalctl` commands**, plus reproducible aggregation of model request timings, throughput and speculative draft acceptance.
- Recorded **14 completed inference requests (20:40:34–20:48:00 CDT)**, **9,206 output tokens**, **22.30 tok/s weighted generation**, **76.45% weighted draft acceptance**, and **457.95 seconds (7m38s) summed measured inference**. **Actual end-to-end task wall time was not measured**; do not report 7m38s as confirmed total session time.
- Added **GPU/CPU/temp/memory telemetry** (`nvtop`, `btop`, `sensors`, sysfs, journal); captured the user's **~98% GPU busy** observation without treating it as an efficiency benchmark.
- Updated the effective context to **65,536** after the user's confirmed `/props` result; original 8K baseline remains documented for comparison. **Automatic start after an actual reboot is still not verified** in the conversation.

### Changes in version 2.1

- Added exact commands for **enabling `llama-qwen.service` at every Ubuntu boot**, verification with `systemctl is-enabled`/`is-active`, and post-reboot HTTP health checks.
- Clarified the difference between **configured/instructed** auto-start and **verified** persistence: at the time of the last captured host output, the unit was still `disabled`; we have not yet received the result of `systemctl enable --now` or a successful reboot test.
- Documented how to reverse auto-start without deleting the service and how to troubleshoot a server that is active but still loading its model.

### Changes in version 2.0

- Verified the separate **1.6G MTP GGUF** is present and that llama.cpp actually loads it.
- Added a **systemd unit** with the exact working Vulkan/MTP launch flags and **manual-start** policy.
- Installed and verified **Cockpit** login; documented its available systemd start/stop/restart/log controls (individual control-button actions not separately captured).
- Verified the **built-in llama.cpp chat interface** works, including a reasoning-enabled reply.
- Captured real **draft acceptance statistics**, memory observations, service logs, and updated performance.
- Corrected previous statements that characterized MTP and persistent service operation as unverified or only proposed.

## 2. Hardware and accelerator verification

These checks should come **before** installing or changing any GPU drivers.

```bash
uname -r
free -h
lspci -nn | grep -Ei 'VGA|Display|3D'
ls -l /dev/kfd /dev/dri/renderD*
cat /proc/cmdline
```

Install diagnostic utilities if missing:

```bash
sudo apt update
sudo apt install -y rocminfo vulkan-tools
```

Inspect AMD compute and Vulkan detection:

```bash
rocminfo | grep -E 'gfx1151|Marketing Name|Name:' | head -20
vulkaninfo --summary
```

**What our computer showed:**

- `rocminfo` identified `AMD RYZEN AI MAX+ 395 w/ Radeon 8060S`, GPU architecture **`gfx1151`**, and `Radeon 8060S Graphics`.
- It also reported `aie2` / `AIE-ML`, corresponding to an AMD AI accelerator device.
- Vulkan listed **`Radeon 8060S Graphics (RADV STRIX_HALO)`**, `driverName = radv`, Mesa version **26.0.8**, and Vulkan API **1.4.335**.
- Vulkan also listed **`llvmpipe`**, a CPU software implementation. Its presence alone does **not** mean the Radeon GPU is being bypassed.
- A `DISPLAY environment variable not set` message appeared over SSH; Vulkan still enumerated the GPU. That warning was not an inference failure.
- `/dev/kfd` and `/dev/dri/renderD128` existed.

**Learning:** Seeing a GPU in `rocminfo`/`vulkaninfo` demonstrates that the stack can enumerate it; it does **not** by itself prove that model layers have been offloaded or that execution is running on the GPU. Confirm actual offloading using llama.cpp loading logs and performance tests.

### Check memory exposed to the graphics stack

```bash
for f in /sys/class/drm/card*/device/mem_info_{vram,gtt}_total; do
  if [ -r "$f" ]; then
    printf '%s: ' "$f"
    numfmt --to=iec < "$f"
  fi
done
```

Observed values:

- `mem_info_vram_total`: **~2 GiB** dedicated UMA allocation.
- `mem_info_gtt_total`: **~61 GiB** of reported GTT capacity.

**Learning:** On this 128 GB unified-memory architecture, the 2 GiB UMA reservation is **not** a hard 2 GiB model-memory ceiling. Dedicated UMA and dynamically accessible shared/GTT memory are different. We intentionally **did not** modify BIOS UMA, GTT/TTM kernel parameters, IOMMU, or swap before establishing a working baseline. Specific engines have different memory requirements; changing these settings without measuring can cause regressions or stability problems.

## 3. Grant the user GPU device access

Check groups:

```bash
id -nG
```

Add the account to the relevant device-access groups:

```bash
sudo usermod -aG render,video "$USER"
```

**Important:** New group membership is **not applied to the already-open SSH shell**. Disconnect and reconnect; if the user also has a desktop session, log out and back in as needed. Then verify:

```bash
id -nG
id "$USER"
```

We initially observed no `render` or `video` groups and later confirmed both were present after reconnecting.

**Learning:** An immediate `id -nG` after `usermod` can still show the old groups. That is normal; it does not mean the modification failed.

## 4. Install Hugging Face CLI and download model files

### Install `pipx` and `hf`

The first attempt to use `pipx` failed because it was unavailable in the active shell. The resolved setup was:

```bash
sudo apt update
sudo apt install -y pipx
pipx ensurepath
# Reconnect / open a new shell if PATH was updated.
pipx install huggingface_hub
hf --version
```

Observed `hf --version`: **2.2.0**.

A suggestion to run `hf skills add -g` was optional and **not required** to download models.

An isolated `sudo: unable to open /etc/sudoers` error occurred earlier, but later `sudo apt` worked normally. No sudoers repair was performed in this work; do not describe that transient error as a confirmed permanent system failure.

### Download the verified Qwen model

```bash
mkdir -p ~/models/qwen3.8-27b

hf download ggml-org/Qwen3.8-27B-GGUF \
  Qwen3.8-27B-Q4_K_M.gguf \
  --local-dir ~/models/qwen3.8-27b

ls -lh ~/models/qwen3.8-27b/
```

**Observed result:** Download completed to:

```text
/home/marinoscar/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf
```

The transfer showed approximately **19.0 GB**, ~**72 MB/s** during the reported download, and `ls -lh` displayed **18G**.

The CLI warned that the request was unauthenticated; despite that, the public-model download succeeded. An HF token may be useful for higher rate limits but was not necessary for this download.

**Learning:** Keep models outside a container's ephemeral filesystem, in a predictable host directory such as `~/models/`. It allows different inference engines to reuse the same downloaded GGUF.

## 5. Build llama.cpp with Vulkan RADV (no Toolbox dependency)

AI Toolbox Cockpit was installed/explored and offered Strix Halo-specific backends, but **the demonstrated successful server was built and run directly from llama.cpp**, without needing Cockpit at runtime.

### Install build prerequisites

```bash
sudo apt update
sudo apt install -y \
  git build-essential cmake ninja-build \
  libvulkan-dev glslc spirv-headers
```

### Clone and build

```bash
cd ~
git clone https://github.com/ggml-org/llama.cpp.git
cd ~/llama.cpp

cmake -S . -B build-vulkan \
  -DGGML_VULKAN=ON \
  -DCMAKE_BUILD_TYPE=Release \
  -DLLAMA_CURL=OFF

cmake --build build-vulkan \
  --config Release \
  --target llama-server llama-cli llama-bench \
  -j 8
```

- `GGML_VULKAN=ON` enables the Vulkan GPU backend.
- `LLAMA_CURL=OFF` disables optional in-engine model downloading; the GGUF was already downloaded with `hf`.
- `-j 8` controls build parallelism; it is not an inference setting.
- The fact that `llama-server` loaded and served requests confirms a working binary was built.

### Confirm device visibility

```bash
~/llama.cpp/build-vulkan/bin/llama-server --list-devices
```

We **did not preserve the output** from this specific command. On a repeat build, save it and inspect model-load logs for actual GPU layer offloading. Ideally the selected device should be the Radeon 8060S Vulkan device and not `llvmpipe`.

### Record build revision for reproducibility

The original git commit was **not captured**. On the current system, capture it now:

```bash
git -C ~/llama.cpp rev-parse HEAD
git -C ~/llama.cpp status --short
~/llama.cpp/build-vulkan/bin/llama-server --version
```

**Learning:** The date alone is insufficient to reproduce benchmarks; llama.cpp evolves quickly. Record the git commit and server version before future upgrades or performance comparisons.

## 6. Start the working baseline server

The initial working launch command:

```bash
~/llama.cpp/build-vulkan/bin/llama-server \
  -m ~/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf \
  -ngl 99 \
  -c 8192 \
  --flash-attn auto \
  --alias qwen3.8-27b \
  --host 127.0.0.1 \
  --port 8080
```

| Flag | Purpose / lesson |
| --- | --- |
| `-m` | Absolute/relative location of the GGUF model. |
| `-ngl 99` | Requests offloading up to 99 layers to the GPU. Actual offload must be checked in logs; specifying a number doesn't prove it occurred. |
| `-c 8192` | Configure the model context for this test. Starting at 8K reduces initial memory requirements versus a very large context. |
| `--flash-attn auto` | Allow llama.cpp to select supported flash attention behavior. |
| `--alias qwen3.8-27b` | Defines the model identifier clients use in API calls. |
| `--host 127.0.0.1` | **Localhost only**. Prevents direct remote access; this early baseline was unauthenticated, unlike the current service. |
| `--port 8080` | HTTP server port. |

**Verified server log excerpts:**

```text
llama_server: model loaded
llama_server: listening on http://127.0.0.1:8080
```

The server also warned:

```text
security: no API key is set and CORS allows all origins
```

**Historical baseline only:** These logs and the launch command above predate API-key protection; the **current authenticated systemd service** is in Section 10 and the setup/security checks are in Section 19. Localhost binding was the safeguard during early testing. Do **not** change `--host` to `0.0.0.0` without encrypted transport and access controls.

### Quick health check

From a second local terminal (leave the server running in the first):

```bash
curl -sS http://127.0.0.1:8080/health
```

Expected when loaded: `{"status":"ok"}`. The user demonstrated successful API inference; the exact health-check output was not preserved.

### Inspect which process has opened the GPU device

```bash
sudo fuser -v /dev/dri/renderD128
```

**Note:** Merely having the GPU device open is weaker evidence than verified layer-offload logs or benchmark behavior. This command was suggested but its output wasn't captured.

## 7. Reasoning-enabled API test (verified working)

Do **not** disable reasoning. The earlier short test with `max_tokens: 300` consumed its output budget in `reasoning_content` and returned empty `content` with `finish_reason: length`. This was **not** an inference crash.

For a model that emits reasoning, allow a larger completion budget. Our successful test used 2,048 maximum output tokens.

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen3.8-27b",
    "messages": [
      {
        "role": "user",
        "content": "You have 9 identical-looking coins. One coin is heavier than the others. Using a balance scale only twice, identify the heavier coin. Explain your strategy step by step."
      }
    ],
    "max_tokens": 2048,
    "temperature": 0.6,
    "stream": false
  }' | python3 -c '
import json, sys
d = json.load(sys.stdin)
m = d["choices"][0]["message"]
print("\n=== REASONING ===")
print(m.get("reasoning_content") or "Not separately returned")
print("\n=== FINAL ANSWER ===")
print(m.get("content") or "No final answer generated")
print("\n=== PERFORMANCE ===")
t = d.get("timings", {})
print("Prompt processing:", round(t.get("prompt_per_second", 0), 2), "tokens/sec")
print("Token generation:", round(t.get("predicted_per_second", 0), 2), "tokens/sec")
print("Generated tokens:", d["usage"]["completion_tokens"])
print("Finish reason:", d["choices"][0]["finish_reason"])
'
```

**Correct solution, demonstrated by the model:** Split nine coins into three groups of three. Weigh one group against another; the heavier group (or the unweighed group, if balanced) contains the heavy coin. Weigh two coins from that identified group; the heavier of the two, or the third if balanced, is the answer. Only two weighings are needed.

**Learning:** Read both `reasoning_content` and `content` in API responses. Output limits count reasoning tokens too. For real coding-agent usage, size the completion budget appropriately; `finish_reason: stop` indicates a complete response, whereas `length` indicates a cut-off.

## 8. Recorded performance results

### Observed measurements

| Test | Prompt processing | Token generation | Output tokens | Result |
| --- | ---: | ---: | ---: | --- |
| Initial simple prompt (`max_tokens: 300`) | 80.57 tok/s | 11.28 tok/s | 300 | Only reasoning emitted; cut off at `length` |
| Nine-coin reasoning test, earlier configuration | 109.26 tok/s | 11.27 tok/s | 349 | Correct final answer; `stop` |
| Nine-coin reasoning test, later configuration | 106.81 tok/s | **24.15 tok/s** | 518 | Correct final answer; `stop`; exact process flags were not preserved with this API output |
| Web UI request with systemd + MTP verified in logs | **243.84 tok/s** | **21.18 tok/s** (server log) | 53 | Web UI showed ~21.59 tok/s; draft model loaded; 65.2% acceptance |
| Later Web UI request (64K active) | **285.46 tok/s** (UI) | **22.43 tok/s** (UI) | 50 | Screenshot; different short request, not a matched test |
| OpenCode coding-agent request window (14 logged requests) | **295.90 tok/s weighted** | **22.30 tok/s weighted** | **9,206** | Aggregate 20:40–20:48; see Section 14 for exact calculations |

**Observed generation speed change:** 24.15 ÷ 11.27 = **2.14×** (approximately **+114%**) relative to the earlier reasoning run.

**Critical qualification:** Later, the user shared the **exact systemd service command** and `journalctl` output, which definitively shows **MTP running and accepting draft tokens**. That establishes MTP activity for the **systemd-backed Web UI test**, not the precise flags used during the separate **24.15 tok/s** test. Even with working MTP, the 11.27→24.15 tok/s difference was **not** measured as a controlled A/B comparison; other configuration and workload differences remain possible.

**Timing interpretation:** Web UI showed approximately 21.59 tok/s; systemd logged `eval time ... 21.18 tokens per second` for the same 53-token answer. For repeatable comparisons, prefer the server-side timings with matched prompts and configurations. The service reported **19.9G** memory in `systemctl status` after loading; this is its observed service-accounted memory, **not a definitive reading of dedicated VRAM use or total shared GPU allocation**.

**Learning:** Performance figures depend on prompt length, context size, batching, model quantization, generated-token count, sampling, reasoning settings, backend, and speculative acceptance. A single API call is a useful baseline but not a comprehensive benchmark. Keep comparisons as controlled as possible.

## 9. MTP speculative decoding — implemented and verified

MTP (multi-token prediction) uses a **smaller draft model** to propose candidate next tokens that the main model validates. Accepted tokens can increase generation throughput without changing the model's final-token distribution in the same way as simply switching to a smaller main model. Whether it helps depends on acceptance, draft overhead, context, and backend performance.

Our Vulkan-enabled binary explicitly supported the option:

```text
--spec-type none,draft-simple,draft-eagle3,draft-mtp,...
--spec-draft-n-max N  number of tokens to draft for speculative decoding
```

### Download the draft model (verified present)

```bash
hf download ggml-org/Qwen3.8-27B-GGUF \
  mtp-Qwen3.8-27B-Q4_0.gguf \
  --local-dir ~/models/qwen3.8-27b

ls -lh ~/models/qwen3.8-27b/*.gguf
```

Confirmed local files:

```text
/home/marinoscar/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf       18G
/home/marinoscar/models/qwen3.8-27b/mtp-Qwen3.8-27B-Q4_0.gguf     1.6G
```

These are `ls -lh` rounded sizes; the original main download was reported as 19.0 GB by Hugging Face.

### MTP-enabled configuration (working flags; **64K effective context verified later**)

```bash
/home/marinoscar/llama.cpp/build-vulkan/bin/llama-server \
  -m /home/marinoscar/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf \
  -md /home/marinoscar/models/qwen3.8-27b/mtp-Qwen3.8-27B-Q4_0.gguf \
  -ngl 99 \
  --spec-draft-ngl all \
  --spec-type draft-mtp \
  --spec-draft-n-max 2 \
  -c 65536 \
  --parallel 1 \
  --flash-attn auto \
  --alias qwen3.8-27b \
  --host 127.0.0.1 \
  --port 8080
```

This reproduces the original MTP flags **with the later confirmed 64K context setting**. The original systemd file was first captured with `-c 8192`; the later HTTP `/props` check returned `65536`. We did **not** receive a fresh `cat /etc/systemd/system/llama-qwen.service` after that change, so verify the persisted unit on the host before treating its literal file text as captured evidence. The binary flags may evolve; check `llama-server --help` if rebuilding. **Do not run an interactive server while systemd uses port 8080.**

### Definitive MTP evidence in `journalctl` (October 12, 2026)

```text
common_speculative_init_result: loading draft model '/home/marinoscar/models/qwen3.8-27b/mtp-Qwen3.8-27B-Q4_0.gguf'
llama_server: model loaded
llama_server: listening on http://127.0.0.1:8080
slot print_timing: draft acceptance = 0.65217 (30 accepted / 46 generated), mean len = 2.30
```

**What this proves:** A separate MTP draft GGUF was loaded, the server was available, and on that request the verifier **accepted 30 of 46 candidate draft tokens**, i.e. about **65.2%**. The mean draft length reported was **2.30**. This is **one request**, not a lifetime acceptance average.

**What it does not prove:** We still have not recorded how many main and draft model layers were actually offloaded to the Radeon GPU, nor completed controlled on/off MTP throughput comparisons across multiple matched runs. The 24.15 tok/s nine-coin test is real, but was not accompanied by contemporaneous startup logs documenting its precise server flags.

### Reproducible A/B experiment (not yet performed)

1. Record llama.cpp commit, binary version, kernel and GPU driver, model checksums.
2. Run the **same** question and settings with MTP enabled and disabled; only change the MTP-specific flags/model.
3. Use warm runs and repeat at least 3 times; track prompt and generation tok/s and total latency.
4. Record `draft acceptance` and `mean len` from service logs, along with the final answer and `finish_reason`.
5. Try `--spec-draft-n-max` values 2, 3, and 4 **one at a time** only after the above baseline.

**Learning:** MTP's successful initialization is now **verified**; attributing an entire speed increase to it still requires controlled tests. Draft acceptance helps explain performance but should not be treated as throughput by itself.

## 10. Run llama.cpp as a systemd service (implemented)

Using `systemd` removes the dependency on an open SSH terminal and allows reliable browser-based controls through Cockpit. The original deployment left automatic startup disabled. The enable command is documented below, but boot persistence has not been independently verified. The **current service enforces a native API key** (Section 19).

### Before creating the unit

Make sure the two GGUFs and compiled binary exist:

```bash
ls -lh /home/marinoscar/models/qwen3.8-27b/*.gguf
ls -l /home/marinoscar/llama.cpp/build-vulkan/bin/llama-server
pgrep -af '[l]lama-server' || true
```

If a manual server is still running on port 8080, stop it with `Ctrl+C` before starting the service. Optionally check the listener with `ss -lntp | grep ':8080'`.

### Create `/etc/systemd/system/llama-qwen.service`

```bash
sudo nano /etc/systemd/system/llama-qwen.service
```

Paste this **current authenticated reconstruction template**. Create the secret first as described in **Section 19**. The initially captured unit contained `-c 8192`; the later deployed unit had the verified `-c 65536` setting:

```ini
[Unit]
Description=Qwen3.8-27B llama.cpp Vulkan MTP Server
After=network.target

[Service]
Type=simple
User=marinoscar
Group=marinoscar
SupplementaryGroups=video render
WorkingDirectory=/home/marinoscar/llama.cpp
LoadCredential=llama-api-key:/etc/llama-qwen/api-key

ExecStart=/home/marinoscar/llama.cpp/build-vulkan/bin/llama-server \
  -m /home/marinoscar/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf \
  -md /home/marinoscar/models/qwen3.8-27b/mtp-Qwen3.8-27B-Q4_0.gguf \
  -ngl 99 \
  --spec-draft-ngl all \
  --spec-type draft-mtp \
  --spec-draft-n-max 2 \
  -c 65536 \
  --parallel 1 \
  --flash-attn auto \
  --alias qwen3.8-27b \
  --host 127.0.0.1 \
  --port 8080 \
  --api-key-file %d/llama-api-key

Restart=on-failure
RestartSec=5
LimitMEMLOCK=infinity

[Install]
WantedBy=multi-user.target
```

The service runs as `marinoscar`, has GPU group memberships, restarts after unexpected failure, binds to loopback, and receives its root-protected API key through a systemd runtime credential. After editing the existing unit, apply `sudo systemctl daemon-reload && sudo systemctl restart llama-qwen` and check `/props` (below). `LimitMEMLOCK=infinity` matches the deployed configuration; its necessity for this model was **not independently benchmarked**.

### Register, start, verify

```bash
sudo systemctl daemon-reload
sudo systemctl start llama-qwen
sudo systemctl status llama-qwen --no-pager -l
sudo journalctl -u llama-qwen -n 150 --no-pager
```

**Observed:** `Active: active (running)`, `Main PID: 14248`, **41 tasks** and **19.9G** systemd-accounted memory two minutes after starting. The server finished loading in ~7 seconds (20:09:10→20:09:17 on the logged clock), then answered the Web UI request. PID and timing are historical examples, not expected constants.

**Do not misread an immediate status check:** At only **13 ms** after startup, `status` showed **1.9M** memory and just 1 task; the model had not loaded yet. Wait for `model loaded`/`listening` and verify `/health` before concluding the service is ready.

### Manual start/stop/restart and logs

```bash
sudo systemctl start llama-qwen
sudo systemctl stop llama-qwen
sudo systemctl restart llama-qwen
sudo systemctl status llama-qwen --no-pager -l
sudo journalctl -u llama-qwen -n 150 --no-pager
sudo journalctl -u llama-qwen -f
```

### Enable automatic startup on every Ubuntu boot (new desired state)

The unit already contains `[Install]` / `WantedBy=multi-user.target`, so it can be enabled **without editing its file**. Run **once** on the MINISFORUM:

```bash
sudo systemctl enable --now llama-qwen.service
```

- `enable` creates the systemd boot-start symlink, persisting across reboots.
- `--now` starts the service immediately if it is not already running; it does **not** need to start a second llama-server.
- This does **not** require `systemctl daemon-reload` unless the unit file has changed.
- The server still listens on **`127.0.0.1:8080`**, so it is reachable by a local browser or SSH tunnel—not directly by other network devices.

Verify **both distinct states**:

```bash
systemctl is-enabled llama-qwen.service  # expected: enabled
systemctl is-active llama-qwen.service   # expected: active
```

**Important evidence boundary (version 3.1):** The last output captured from the machine showed the service **active but disabled**. The `enable --now` command has been **provided**, but its execution and boot persistence **have not yet been independently confirmed**. After running it, record the `is-enabled` result and perform one test reboot when convenient.

#### Reboot smoke test

Restart Ubuntu normally after saving work (GNOME may inhibit CLI reboot if desktop sessions are active). **Do not confuse systemd reporting `active` with the model being fully loaded**: wait for `model loaded` in the logs or a healthy endpoint.

```bash
systemctl is-enabled llama-qwen.service
systemctl is-active llama-qwen.service
sudo systemctl status llama-qwen --no-pager -l
curl -fsS http://127.0.0.1:8080/health
```

Expected `systemctl` output: `enabled` and `active`. Once loading has finished, `/health` should return `{"status":"ok"}`. If `curl` fails shortly after boot, give llama.cpp time to load both GGUF files and retry. To see errors and startup timing:

```bash
sudo journalctl -u llama-qwen -b -n 150 --no-pager
```

Because the service runs as the system user `marinoscar`, this boot startup **does not require logging in to GNOME or keeping an SSH session open**. Cockpit can still stop/start/restart it at any time. `Restart=on-failure` restarts unexpected crashes but does not negate an intentional stop from Cockpit.

#### Return to manual-only mode if needed

```bash
sudo systemctl disable --now llama-qwen.service
```

This disables future boot startup **and stops the current server**. `sudo systemctl disable llama-qwen.service` alone disables boot startup but **leaves an already-running server running**. Both leave model files and the unit file intact.

**When editing the service:** Update the file, run `sudo systemctl daemon-reload`, then `sudo systemctl restart llama-qwen`. **Don't start a second manual server on the same port.**

## 11. Browser UIs: Cockpit administration and llama.cpp chat (implemented)

The chosen setup uses **two different web applications**, each with a separate role:

| Interface | Address on the AI workstation | Purpose |
| --- | --- | --- |
| **Cockpit** (Linux admin web console; not AI Toolbox Cockpit) | `https://localhost:9090` | Manage `llama-qwen.service` and inspect logs/status |
| **Built-in llama.cpp Web UI** | `http://127.0.0.1:8080` | Chat with Qwen; reasoning, latency, token throughput |
| **llama.cpp OpenAI-compatible API** | `http://127.0.0.1:8080/v1` | Programmatic calls for agents and applications |

### Install and use Cockpit

```bash
sudo apt update
sudo apt install -y cockpit
sudo systemctl enable --now cockpit.socket
sudo systemctl status cockpit.socket --no-pager
```

**Verified:** Cockpit's Overview opened in the browser on Ubuntu 26.04.1 LTS and displayed the MINISFORUM MS-S1 Max machine. The Cockpit **Services** tab was also opened, although individual start/stop buttons were not exercised in the shared evidence. The Web UI initially displayed **"Limited access"** and **"Turn on administrative access"**. Elevate permissions through that button when managing system services, then navigate to **Services** and search for `llama-qwen.service`.

From Cockpit's service page, use **Start**, **Stop**, **Restart** and the service's **Logs**. Cockpit is an administrator interface and must not be left open to the public internet. It normally uses HTTPS and may initially show a locally generated/untrusted certificate. Verify the hostname/certificate context before proceeding in your browser.

If connecting from another device over Tailscale, `https://<TAILSCALE-IP>:9090` is only appropriate **when the device is authenticated to your tailnet and firewalls/ACLs permit the connection**. Remote Tailscale access to Cockpit was discussed but **not independently demonstrated** in the captured session.

### Use the built-in llama.cpp chat

With `llama-qwen.service` running, open:

```text
http://127.0.0.1:8080
```

**Verified:** Browser chat answered the test prompt "What's your name?" with **"I'm Qwen! How can I help you today?"**, showing a collapsible **Reasoning** section and throughput metrics. The UI displayed about **21.59 tok/s** generation and **243.84 tok/s** prompt processing; the server journal reported **21.18 tok/s** generation for the request.

From another PC, `127.0.0.1` refers to **that PC**, not the MINISFORUM. Until we configure authenticated remote serving, connect through an SSH tunnel rather than exposing the model port:

```bash
# Run on the client PC, substituting the MINISFORUM's reachable host/IP:
ssh -N -L 18080:127.0.0.1:8080 marinoscar@MINISFORUM_HOST
# Then browse locally on the client PC to http://127.0.0.1:18080
```

### Verify from logs and HTTP

```bash
curl -sS http://127.0.0.1:8080/health
sudo journalctl -u llama-qwen -n 150 --no-pager | grep -E 'draft model|draft acceptance|model loaded|listening|eval time'
```

The `/health` endpoint was verified returning `{"status":"ok"}` after the authenticated service restarted. It checks readiness, **not** API authorization.

**Security learned:** The chat-completions API now requires an API key (verified **401 without** and **200 with**); the Web UI can authenticate. `/health` and `/v1/models` must **not** be used as security tests. Continue binding to loopback until encrypted and restricted remote access is designed. **Cockpit does not authenticate llama.cpp calls.** See Section 19.

## 12. OpenCode — local coding agent connected to llama.cpp (verified)

**Goal:** Run a coding agent with tools (read/write/edit/shell), using **our existing** native llama.cpp backend and Qwen GGUF rather than installing Ollama. The test was performed **on AI-MARIN-LINUX**, so `127.0.0.1:8080` was reachable from OpenCode without a tunnel.

### Install OpenCode and verify it

```bash
curl -fsSL https://opencode.ai/install | bash
# Start a new shell/reconnect if 'opencode' is not in PATH.
opencode --version
```

**Evidence:** OpenCode's UI was displayed with **Build** mode and **Qwen3.8-27B Q4_K_M** selected; it subsequently performed file and shell tool calls. We did **not** capture `opencode --version`; record it on the host for future exact reproduction. Installation and setup commands below were provided; the running UI and stats confirm operational integration, **not** the contents of every current config field.

### Local provider configuration (rebuild example)

Verify that the local inference service is available:

```bash
systemctl is-active llama-qwen
curl -sS http://127.0.0.1:8080/v1/models | python3 -m json.tool
curl -sS http://127.0.0.1:8080/props | python3 -c \
  'import json,sys; d=json.load(sys.stdin); print(d["default_generation_settings"]["n_ctx"])'
# The user's /props output was exactly: 65536
```

Global provider file (`~/.config/opencode/opencode.json`):

```bash
mkdir -p ~/.config/opencode
nano ~/.config/opencode/opencode.json
```

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "local-llama/qwen3.8-27b",
  "small_model": "local-llama/qwen3.8-27b",
  "share": "disabled",
  "provider": {
    "local-llama": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "MINISFORUM Local AI",
      "options": {
        "baseURL": "http://127.0.0.1:8080/v1",
        "apiKey": "{file:~/.config/opencode/llama-api-key}",
        "timeout": 600000
      },
      "models": {
        "qwen3.8-27b": {
          "name": "Qwen3.8-27B Q4_K_M",
          "limit": {
            "context": 65536,
            "output": 8192
          }
        }
      }
    }
  }
}
```

**Important:** This is the **authenticated configuration pasted by the user**, and authenticated OpenCode interaction was subsequently verified. Create the private file as in Section 19; the `{file:...}` expression is a file reference, not the literal key. Model ID `qwen3.8-27b` must match `llama-server --alias qwen3.8-27b`. The 8,192 output-token setting is the *per-response maximum OpenCode should budget*, not the whole context. The 64K context includes prompts, tool outputs, reasoning and generated responses.

Inspect available models:

```bash
opencode models local-llama
```

**Observed:** The interactive UI displayed `Qwen3.8-27B Q4_K_M` and performed tasks; the literal output of `opencode models` was not captured. Avoid claiming this command was independently verified.

### Launch the tested workspace

```bash
mkdir -p ~/projects/opencode-test
cd ~/projects/opencode-test
# Earlier, this README was suggested for a smoke test:
printf '# Test Project\nThis is a Python calculator project.\n' > README.md
opencode
```

The live screenshot showed **Build mode** (not merely Ask/chat mode), with the model identified as **Qwen3.8-27B Q4_K_M** and working directory `~/projects/opencode-test`. The `README.md` setup was *suggested*; whether the user executed it before the calculator prompt was not captured.

### Using OpenCode on a separate development PC (planned, not tested)

Since llama.cpp binds `127.0.0.1`, it is **not** directly accessible from other LAN/Tailscale devices. Tunnel to it from a separate workstation:

```bash
ssh -N -L 18080:127.0.0.1:8080 marinoscar@YOUR_MINISFORUM_TAILSCALE_IP
# Run from the development PC, in another terminal:
curl -sS http://127.0.0.1:18080/health
```

On that development PC, set OpenCode `options.baseURL` to `http://127.0.0.1:18080/v1`. A remote OpenCode instance also needs a private key copy. Do not bind the service to `0.0.0.0` without TLS, authentication, firewall and ACL planning. An SSH tunnel does **not** automatically start a model server: ensure systemd is active. This remote OpenCode path remains **unverified**.

## 13. Real-world coding test — exact prompt, artifact, and OpenCode statistics (October 12, 2026)

### Reproduce the calculator test

**Location:** `/home/marinoscar/projects/opencode-test`  
**Agent mode:** Build  
**Model:** `local-llama/qwen3.8-27b` / Qwen3.8-27B Q4_K_M  
**Inference:** Native llama.cpp Vulkan + MTP draft model, `--parallel 1`; **64K effective context** verified later with `/props` (exact context in effect during every earlier subrequest was not independently logged).  
**Exact user prompt from OpenCode screenshot:**

> Create an HTML page that is a calculator, like the one you get on my iphone

**Observed output:** `~/projects/opencode-test/calculator.html`, opened in Chrome at `file:///home/marinoscar/projects/opencode-test/calculator.html`. The rendered interface had a black background, gray round number/function buttons, orange operators, a wide zero key, and a large right-aligned numeric display (screenshot showed `10,710`). **Visual rendering and file creation were confirmed.** We did not receive automated test results or independently exercise all numeric cases; **do not claim full functional correctness** for chained operations, rounding, percentages, divide-by-zero, accessibility or mobile responsiveness.

**OpenCode output/statistics command actually used:**

```bash
cd ~/projects/opencode-test
opencode stats --project "" --models
```

The user's screenshot reported:

| Metric | Observed | Interpretation / caveat |
| --- | ---: | --- |
| Sessions | 1 | In the report scope, one session |
| Messages | 14 | OpenCode's session-message count |
| Days | 1 | Activity spans one reported day |
| Average / median tokens per session | 196.4K / 196.4K | Cumulative usage (not a single-context size) |
| **Input tokens** | **12.8K** | Uncached input as reported by OpenCode |
| **Output tokens** | **9.0K** | Rounded UI figure; server logs provide an exact 9,206 sum in the selected request window |
| **Cache read** | **174.7K** | Cumulative cached-token accounting, **not** proof that a 174K-token context was loaded in one request |
| Cache write | 0 | As reported by OpenCode; provider-specific accounting |
| Reported model messages | 13 | Model usage row; differs from 14 total session messages |
| Cost | $0.00 | No metered provider API charges; does not include hardware purchase or electricity |
| **Tool calls** | **16** | Nine edits, three shell, three reads, one write |

Tool breakdown:

| Tool | Count | Percent of 16 |
| --- | ---: | ---: |
| `edit` | 9 | 56.3% |
| `bash` | 3 | 18.8% |
| `read` | 3 | 18.8% |
| `write` | 1 | 6.3% |

**Learning:** Working agent tool calls are now demonstrated, not hypothetical. But nine `edit` calls do not mean nine independent fixes, and three `bash` invocations are not necessarily unit tests. The exported session detail is needed to determine precise tools, error rates and correction loops.

### Commands to inspect/replay an individual OpenCode session

These were **recommended for deeper investigation**, but no `session list` or `export` output was provided:

```bash
cd ~/projects/opencode-test
opencode session list
opencode export SESSION_ID > ~/calculator-run.json
# OpenCode session JSON may include prompts, code, paths or secrets; inspect before sharing.
```

Within a running OpenCode TUI, `/details` was suggested to expose tool execution details; this command's behavior was **not independently verified** in the screenshots, and may depend on OpenCode version. Prefer the session export for auditable details. Run `opencode --help` and `opencode stats --help` if CLI flags differ after upgrades.

### Task duration — distinguish inference time from actual elapsed time

**What was recorded:** 14 completed llama.cpp request records between **20:40:34 and 20:48:00 CDT on October 12, 2026**, a **7m26s span between the timestamps of the first and last recorded completions**. The **sum of model-reported prompt evaluation and generation** across these requests was **457.95 seconds ≈ 7 minutes 38 seconds**. This can exceed the completion-timestamp span because the first request began before its 20:40:34 completion.

**What was NOT recorded:** The exact moment the user submitted the prompt, OpenCode's exact task-completion timestamp, total orchestration/tool-execution time, or a separately timed full end-to-end wall-clock stopwatch. **Do not label 7m38s as confirmed full-task duration.** For a new run, use `date -Is` immediately before submitting the prompt and at completion, or capture OpenCode session timestamps from exported event data.

## 14. Server-side coding-run analysis — commands, calculations, and raw observations

### Capture llama.cpp request timings (command actually run)

```bash
sudo journalctl -u llama-qwen --since "2 hours ago" --no-pager \
  | grep -E 'prompt eval time|eval time|draft acceptance'
```

The user pasted this output for the October 12 run. To archive an immutable future reference, use **absolute timestamps** instead of a rolling two-hour query:

```bash
sudo journalctl -u llama-qwen \
  --since "2026-10-12 20:40:00" --until "2026-10-12 20:49:00" \
  --no-pager > ~/projects/opencode-test/llama-calculator-2026-10-12.log
```

The exact aggregate below includes **14 completion records** from `20:40:34` through `20:48:00` in the user-pasted output (PID 16986). The earlier logs at 20:09, 20:24, and 20:31 were excluded—they were unrelated earlier chat tests.

| Aggregate from 14 completed inference requests | Value |
| --- | ---: |
| **Main-model generated tokens** | **9,206** |
| Prompt tokens newly evaluated (server metric) | **13,364** |
| Main-token evaluation time | **412,789.39 ms** (6m52.79s) |
| Prompt evaluation time | **45,163.32 ms** (45.16s) |
| **Total measured inference** | **457,952.71 ms** (7m37.95s) |
| **Weighted generation throughput** | **22.30 tok/s** (= 9206 / 412.78939s) |
| **Weighted prompt-evaluation throughput** | **295.90 tok/s** (= 13364 / 45.16332s) |
| MTP accepted tokens | **5,561** |
| MTP generated candidate tokens | **7,274** |
| **Weighted draft acceptance** | **76.45%** (= 5561 / 7274) |
| Best single-request generation | **25.46 tok/s**, 3,100 tokens, task 718 at 20:44:02 |
| Slowest single-request generation | **18.63 tok/s**, 207 tokens, task 3617 at 20:48:00 |
| Best acceptance | **93.21%** (2017 / 2164 candidate draft tokens) on task 718 |

**Why these averages:** Token-weighted throughput divides the *sum of actual tokens* by the *sum of inference execution time*, not by the number of requests and not by a simple arithmetic mean of tok/s figures. Draft acceptance weights by **candidate token counts**. Reported llama.cpp evaluation speeds can differ from end-to-end OpenCode speeds because they exclude file I/O, tool orchestration and client waits.

### Per-request log snapshot (preserved benchmark evidence)

Values below are transcribed from the user's actual `journalctl` output; `tok/s` is the **displayed llama.cpp per-request value**. Aggregates above divide total recorded tokens by total evaluation milliseconds, so tiny discrepancies with logged per-request speed can arise from llama.cpp's own timing/token conventions.

| Completed (CDT) | Newly evaluated prompt tokens | Generated tokens | llama.cpp generation tok/s | Accepted / drafted MTP tokens |
| --- | ---: | ---: | ---: | ---: |
| 20:40:34 | 604 | 214 | 19.34 | 110 / 206 |
| 20:42:00 | 7,850 | 1,289 | 20.13 | 712 / 1,152 |
| 20:44:02 | 81 | 3,100 | 25.46 | 2,017 / 2,164 |
| 20:44:25 | 21 | 474 | 20.80 | 273 / 400 |
| 20:44:30 | 28 | 91 | 19.79 | 51 / 80 |
| 20:44:56 | 19 | 596 | 23.20 | 371 / 450 |
| 20:45:03 | 37 | 147 | 24.21 | 93 / 106 |
| 20:45:11 | 20 | 190 | 24.78 | 122 / 134 |
| 20:45:19 | 20 | 152 | 20.94 | 89 / 126 |
| 20:46:12 | 1,642 | 1,010 | 21.04 | 593 / 834 |
| 20:46:20 | 19 | 171 | 23.44 | 107 / 126 |
| 20:46:53 | 1,172 | 615 | 21.99 | 374 / 482 |
| 20:47:41 | 37 | 950 | 19.74 | 537 / 826 |
| 20:48:00 | 1,814 | 207 | 18.63 | 112 / 188 |

### Recalculate a fresh run from the saved journal file (new reusable command)

```bash
# Journal saved to the file above. Run from the MINISFORUM:
python3 - ~/projects/opencode-test/llama-calculator-2026-10-12.log <<'PY'
import re, sys
from pathlib import Path
text = Path(sys.argv[1]).read_text()
req = {}
for line in text.splitlines():
    task = re.search(r'\| task\s+(\d+)\s+\|', line)
    if not task or 'print_timing:' not in line:
        continue
    key = task.group(1)
    row = req.setdefault(key, {})
    m = re.search(r'\|\s+prompt eval time\s+=\s+([\d.]+) ms /\s+(\d+) tokens', line)
    if m:
        row.update(prompt_ms=float(m[1]), prompt_tokens=int(m[2]))
    m = re.search(r'\|\s+eval time\s+=\s+([\d.]+) ms /\s+(\d+) tokens', line)
    if m:
        row.update(generation_ms=float(m[1]), generation_tokens=int(m[2]))
    m = re.search(r'draft acceptance\s+=\s+[\d.]+\s+\(\s*(\d+) accepted /\s*(\d+) generated', line)
    if m:
        row.update(accepted=int(m[1]), drafted=int(m[2]))
complete = [r for r in req.values()
            if {'prompt_ms','prompt_tokens','generation_ms','generation_tokens'} <= r.keys()]
if not complete:
    raise SystemExit('No complete request timing records found; inspect journal format.')
prompt_ms = sum(r['prompt_ms'] for r in complete)
gen_ms = sum(r['generation_ms'] for r in complete)
prompt_tokens = sum(r['prompt_tokens'] for r in complete)
gen_tokens = sum(r['generation_tokens'] for r in complete)
accepted = sum(r.get('accepted', 0) for r in complete)
drafted = sum(r.get('drafted', 0) for r in complete)
print(f'Completed requests: {len(complete)}')
print(f'Prompt: {prompt_tokens} tokens / {prompt_ms/1000:.2f}s = {prompt_tokens/(prompt_ms/1000):.2f} tok/s')
print(f'Generation: {gen_tokens} tokens / {gen_ms/1000:.2f}s = {gen_tokens/(gen_ms/1000):.2f} tok/s')
print(f'Measured inference total: {(prompt_ms + gen_ms)/1000:.2f}s')
print(f'MTP draft acceptance: {accepted}/{drafted} = {100*accepted/drafted:.2f}%' if drafted else 'No draft stats')
PY
```

**Caveat:** This simple analyzer groups events by `task` ID inside one bounded journal/log window. If you aggregate logs across llama.cpp restarts where task IDs repeat, group by **PID + task ID** or analyze each process/run separately. If the server streams overlapping requests, ensure the bounded window and request grouping correspond to the workload being measured.

### Interpreting the results and the cache numbers

- **High MTP acceptance** (76.45% overall; one 93.21% request) confirms substantial speculative reuse. It is *not* a measurement of a 76% speed increase. A controlled MTP-off run is still needed to attribute speed gain.
- Many requests show **only 19–81 prompt tokens freshly evaluated**. That is consistent with reusing prior context/KV during a sequence of OpenCode calls, but the logs do not by themselves prove the exact cache implementation or that all `174.7K` OpenCode cache-read tokens were physically skipped on the server.
- One request evaluated **7,850 prompt tokens** over ~21.2 seconds; large new prompt chunks cause visible delay even when generating 22+ tok/s.
- The measured ~7m38s inference time includes prompt processing and generation for these 14 requests, **not** agent tool execution / retries / final wall time.
- The stats tool reports **14 messages and 13 model messages**; the journal shows **14 completed inference records**. These are different accounting systems and should not be assumed one-to-one without correlating session IDs and timestamps.

## 15. GPU, CPU, memory and thermal monitoring during OpenCode

While OpenCode ran, the user reported **approximately 98% GPU utilization** and audible but not excessive cooling-fan activity. This is consistent with substantial GPU work, but does not establish the percentage of model layers offloaded, frequency/thermal throttling, actual power draw or tokens per joule. No sensor temperature or power trace was captured.

Run telemetry from a **second SSH session** while OpenCode runs:

```bash
# Fast built-in instantaneous utilization check (sysfs path was valid for this machine):
watch -n 1 'cat /sys/class/drm/card1/device/gpu_busy_percent'

# Optional interactive dashboards:
sudo apt install -y nvtop btop lm-sensors
nvtop                   # GPU utilization, memory/processes if exposed by driver
btop                    # CPU/RAM and process view
watch -n 2 sensors      # Temperatures, if supported
```

Inspect graphics memory usage separately from total system RAM:

```bash
watch -n 2 '
for type in vram gtt; do
  f=/sys/class/drm/card1/device/mem_info_${type}_used
  if [ -r "$f" ]; then
    printf "%s used: " "$type"
    numfmt --to=iec < "$f"
  fi
done'

free -h
systemctl status llama-qwen --no-pager -l
```

**Learning:** 98% `gpu_busy_percent` during a sampling interval shows the GPU was busy, **not** that the system achieved its maximum possible token speed or that the entire 128 GB is allocated as VRAM. For future A/B tests, log GPU temperature, clocks, package power, memory allocation, utilization, llama.cpp commit/model hashes, draft acceptance and final correctness. Cooling response is expected under prolonged load, but fan noise alone is not a temperature measurement.

## 16. Interpretation, reproducible comparison, and next benchmark

**Assessment:** This is a **successful first local coding-agent capability test** and **usable but not instant** speed. The model created a visually convincing iPhone-inspired calculator and completed tool workflows autonomously. The observed weighted **22.30 tok/s generation**, high MTP acceptance and ~98% observed GPU utilization show the software and GPU are being exercised. Do **not** extrapolate from this one task to multi-repository refactors or assume the calculator passes a complete functional test suite.

**Suggested controlled repeat:**

1. Save current `opencode --version`, `llama-server --version`, GGUF SHA-256 hashes, model/engine flags, `opencode.json`, and active `n_ctx` from `/props`.
2. Start a **fresh OpenCode session in a clean test directory** and use the **exact same calculator prompt** in Section 13; fix approval policy and test acceptance criteria.
3. Record prompt submission `date -Is`, first/last model request times, final agent-completion `date -Is`, and `opencode session list`/export metadata for the session.
4. Archive OpenCode stats and a bounded **journalctl** export; run the summarizer in Section 14. Record counts, costs, tokens, tool-call errors and total wall-clock duration.
5. Validate functional correctness: basic arithmetic, sign toggling, percentage, decimal points, division by zero, chaining operations, Clear, keyboard/mobile behavior. Prefer browser-driven tests or JS unit tests if available.
6. Change **only one variable per run** (MTP off/on, draft depth 2/3/4, engine Vulkan/ROCm, GGUF quantization or alternative model), repeat 3+ times, and compare **time to correct working artifact**—not just raw tok/s.

**Next priority:** Before micro-tuning the GPU, compare an efficient alternate coding model against this unchanged Qwen baseline using the same prompt and measured outcome. Optimize for **correct completed tasks/hour**, tool-call reliability and wall time; a faster model that requires many corrections may be worse overall.

## 17. Troubleshooting guide

| Symptom | What it meant here | What to do |
| --- | --- | --- |
| `pipx: command not found` | CLI tool not available in current shell at first attempt | `sudo apt install -y pipx`; `pipx ensurepath`; reopen shell if necessary |
| `usermod` succeeded, but `id -nG` unchanged | Existing login retained old groups | Disconnect/reconnect SSH (and desktop session if needed) |
| `DISPLAY environment variable not set` in `vulkaninfo` | SSH/headless surface query warning | Check GPU device enumeration; it did not block Vulkan detection |
| Both RADV and `llvmpipe` listed | Vulkan exposes real AMD GPU and CPU software device | Ensure inference chooses Radeon, not llvmpipe; verify offload/logs |
| `content` empty but `reasoning_content` populated | Output budget exhausted before final answer | Increase `max_tokens`; check `finish_reason` |
| `finish_reason: length` | Generation stopped at token cap | Raise token cap or use a shorter problem |
| Missing `reasoning_content` on a future build | API/template/runtime representation may differ | Inspect raw JSON and current llama.cpp reasoning options |
| Low tok/s | Could be backend, offload, context, power, quantization, MTP state | Record logs and benchmark before modifying BIOS or drivers |
| `sudoers` error once | Transient, later sudo commands succeeded | If recurrent, investigate; no persistent repair was needed here |
| No API key / CORS warning | Unauthenticated llama-server defaults | Keep loopback binding during development; secure before remote exposure |
| Immediate `systemctl status` says ~1.9M memory | Service only 13 ms into startup; model not loaded yet | Wait for `model loaded` and HTTP health before calling it ready |
| Service is `disabled` but `active (running)` | `disabled` means no automatic startup, not currently stopped | If persistent auto-start is desired, run `sudo systemctl enable --now llama-qwen.service`; verify with `is-enabled` |
| `enabled` but server inaccessible immediately after reboot | Unit started but GGUF models may still be loading, or startup failed | Wait for `/health` = `{"status":"ok"}`; inspect `journalctl -u llama-qwen -b -n 150` |
| Still `disabled` after intended change | Enable command not run or failed | `sudo systemctl enable --now llama-qwen.service`, then `systemctl is-enabled llama-qwen` |
| Service fails after editing unit | systemd still has an old unit definition or bad paths | `daemon-reload`, inspect `status` and `journalctl`; verify both GGUF paths |
| Port 8080 already in use | Interactive and systemd servers may conflict | Stop foreground process first; check with `pgrep` and `ss -lntp` |
| Remote browser can't reach `127.0.0.1:8080` | Loopback resolves on the client device, not the AI host | Use an SSH tunnel now; add authenticated remote serving later |
| Cockpit shows "Limited access" | Web session isn't elevated | Click **Turn on administrative access** before controlling system services |

## 18. Operational and security lessons

- **Backend:** We deliberately started with Vulkan RADV because the installed AMD Vulkan stack was visible and functional. ROCm was detected but **not** benchmarked with this model; it remains a comparison to run.
- **Containers optional:** AI Toolbox Cockpit was explored but not necessary for a working model server. A native build offers transparent control of flags and startup logs.
- **Running state:** The model server is managed by a **verified systemd service**, independent of SSH, with `Restart=on-failure`. Its previously observed boot policy was **disabled**; Section 10 now documents `enable --now` for the **requested new auto-start policy**. Its actual enabled state and post-reboot operation are **pending user verification**. An unmanaged foreground process could still be terminated by an SSH disconnect.
- **Exposure:** `127.0.0.1:8080` remains local to the AI computer. **The model chat endpoint now requires a valid API key** (verified 401/200), and both OpenCode and the built-in Web UI successfully authenticated. Cockpit on port 9090 uses separate admin authentication. For remote use, add transport encryption and network ACLs in addition to API-key protection.
- **Management UI tradeoff:** llama.cpp's built-in Web UI was **already included** with `llama-server` and is sufficient for basic interactive chat. Cockpit controls the **Linux service**, not the model's conversation state. A richer ChatGPT-style frontend (e.g. Open WebUI) was discussed but **not installed or validated**.
- **MTP now verified:** Draft-model initialization and nonzero accepted tokens were captured. Do not conflate **MTP active** with **MTP definitely caused the entire 2.14× change** absent A/B testing.
- **Systemd accounting:** 19.9G in service status is systemd's service memory accounting; it is not proof of total GPU memory used or how many layers were offloaded.
- **Resources:** The ~2 GiB dedicated UMA allocation and ~61 GiB GTT exposure were left unchanged during the confirmed working setup. Avoid assuming that a huge dedicated-UMA reservation always improves Strix Halo inference.
- **Reproducibility:** Save model filename/quantization, SHA-256 checksum, `llama.cpp` git commit, command line, driver/kernel version, and benchmark conditions before further tuning.

Suggested future inventory commands:

```bash
sha256sum ~/models/qwen3.8-27b/Qwen3.8-27B-Q4_K_M.gguf
git -C ~/llama.cpp rev-parse HEAD
~/llama.cpp/build-vulkan/bin/llama-server --version
uname -r
vulkaninfo --summary | grep -E 'deviceName|driverName|driverInfo'
```

## 19. API-key security for llama.cpp, OpenCode and Web UI — verified (October 12, 2026)

**Goal:** Protect the Qwen3.8-27B inference API without disturbing Vulkan, MTP, the 65,536-token context, or systemd. llama.cpp remains bound to `127.0.0.1:8080`; Cockpit is a separate admin interface on port 9090. **Confirmed outcome:** HTTP **401** for an unauthorized chat request; HTTP **200** with the valid Bearer token; OpenCode and the built-in Web UI subsequently worked with authentication. **Never store a real key in this document.**

### 19.1 Generate and protect the API key

On the MINISFORUM, run once:

```bash
sudo install -d -m 700 /etc/llama-qwen
sudo install -m 600 -o root -g root /dev/null /etc/llama-qwen/api-key
openssl rand -hex 32 | tr -d '\n' | sudo tee /etc/llama-qwen/api-key > /dev/null
```

This generates **32 bytes (256 bits)** of random material, represented as 64 hexadecimal characters, at `/etc/llama-qwen/api-key`. The directory and key file are restricted to root. This command **overwrites the previous key if repeated**; treat that as a credential rotation, requiring updates to OpenCode and the browser UI.

Optional permissions check, without disclosing the secret:

```bash
sudo stat -c '%a %U:%G %n' /etc/llama-qwen /etc/llama-qwen/api-key
# Expected: 700 root:root for directory; 600 root:root for file
```

### 19.2 Inject the secret securely into the existing systemd service

Edit `/etc/systemd/system/llama-qwen.service`:

```bash
sudo nano /etc/systemd/system/llama-qwen.service
```

In `[Service]` include:

```ini
LoadCredential=llama-api-key:/etc/llama-qwen/api-key
```

The last lines of the existing, multi-line `ExecStart` are:

```ini
  --alias qwen3.8-27b \
  --host 127.0.0.1 \
  --port 8080 \
  --api-key-file %d/llama-api-key
```

Retain **every other working argument**, including the main/draft GGUF paths, `-ngl 99`, `--spec-draft-ngl all`, `--spec-type draft-mtp`, `--spec-draft-n-max 2`, `-c 65536`, `--parallel 1`, and `--flash-attn auto` (full, updated service template in **Section 10**). systemd loads the root-owned file as a **service credential**; `%d` resolves to the service's private runtime credential directory, so the literal key does not appear in the unit file or the process command line.

Validate and apply:

```bash
sudo systemd-analyze verify /etc/systemd/system/llama-qwen.service
sudo systemctl daemon-reload
sudo systemctl restart llama-qwen.service
systemctl status llama-qwen.service --no-pager -l
curl -fsS http://127.0.0.1:8080/health
```

**Observed:** validation produced no errors; the service restarted; `/health` returned `{"status":"ok"}`. The restart kept the model accessible and did not require a desktop login.

### 19.3 Verify protected inference with and without the key

**Do not use `/health` or `/v1/models` as authorization tests**; the latter returned HTTP 200 unauthenticated and some informational endpoints remain public. Test the actual model inference route.

Without key (**verified HTTP 401**):

```bash
curl -sS -o /dev/null -w 'HTTP Status: %{http_code}\n' \
  http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.8-27b","messages":[{"role":"user","content":"Hello"}],"max_tokens":1}'
```

With key (**verified HTTP 200**):

```bash
API_KEY="$(sudo cat /etc/llama-qwen/api-key)"
curl -sS -o /dev/null -w 'HTTP Status: %{http_code}\n' \
  http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.8-27b","messages":[{"role":"user","content":"Hello"}],"max_tokens":1}'
unset API_KEY
```

The local shell variable is cleared immediately after testing. Both status codes were explicitly captured during setup. This verifies authorization on **this protected route**; don't assume every informational endpoint is gated.

### 19.4 Make OpenCode use a private credential file

OpenCode runs as user `marinoscar` and must not read the root-only key file directly. Install a separate user-readable copy:

```bash
mkdir -p ~/.config/opencode
sudo install -m 600 -o marinoscar -g marinoscar \
  /etc/llama-qwen/api-key \
  /home/marinoscar/.config/opencode/llama-api-key
stat -c '%a %U:%G %n' ~/.config/opencode/llama-api-key
# Expected: 600 marinoscar:marinoscar .../llama-api-key
```

In `~/.config/opencode/opencode.json`, retain the existing provider/model configuration, but set the provider's `options` to:

```json
"options": {
  "baseURL": "http://127.0.0.1:8080/v1",
  "apiKey": "{file:~/.config/opencode/llama-api-key}",
  "timeout": 600000
}
```

The **full verified OpenCode JSON** is preserved in Section 12, including model `local-llama/qwen3.8-27b`, context 65536, and output budget 8192. The `{file:...}` syntax resolves the local secret without embedding it in the JSON. **Verified:** After this change the user ran OpenCode, asked **“hi, what's your name?”**, and the model returned **“Qwen.”** The screenshot showed 34.5 seconds for that OpenCode interaction; that is **client-reported elapsed time**, not a model-only throughput benchmark.

Suggested repeat check:

```bash
cd ~/projects/opencode-test
opencode run "What is 2 + 2? Answer briefly."
```

This command is a reusable check; the observed authenticated proof came from the interactive OpenCode session.

### 19.5 Authorize the built-in llama.cpp Web UI

1. Open **`http://127.0.0.1:8080/`** in a browser on the MINISFORUM (or through a secure SSH tunnel).
2. Refresh. The built-in UI prompts for the API key; if needed, find the key setting under its Settings menu.
3. On a **trusted local** terminal, retrieve the value with `sudo cat /etc/llama-qwen/api-key`; enter it in the browser. Do **not** include the key in screenshots or the runbook.
4. Start a chat and check that Qwen responds. **The user confirmed success.**

The browser may retain credentials in local site settings. Only use a trusted browser/profile and clear saved credentials on shared devices. The built-in chat UI is a client, **not** a multi-user authentication service. Cockpit has independent Ubuntu-user authentication.

### 19.6 Persist across restart and verify after reboot

`LoadCredential` and `--api-key-file` are in the systemd unit and therefore persist across service restarts. To request **boot-time startup**:

```bash
sudo systemctl enable --now llama-qwen.service
systemctl is-enabled llama-qwen.service  # expected: enabled
systemctl is-active llama-qwen.service   # expected: active
```

After reboot, wait for the model to finish loading, then verify:

```bash
curl -fsS http://127.0.0.1:8080/health
# Repeat the 401/200 chat-completions checks from Section 19.3
sudo journalctl -u llama-qwen -b -n 80 --no-pager
```

**Evidence boundary:** An authenticated **service restart** was verified, but the conversation did not capture `is-enabled` output or a successful **post-reboot** security smoke test. Do not mark a reboot test completed until observed.

### 19.7 Security lessons and future rotation

- **A valid key is required for the protected inference endpoint**, but a key does **not** encrypt transport. Keep using `127.0.0.1`, or a secure SSH tunnel over Tailscale, until an HTTPS gateway and access policy are set up for remote clients.
- A separate key file exists for OpenCode; **both copies must be updated on rotation**. To rotate, regenerate the root key as in 19.1, recopy it as in 19.4, restart the model service, and re-enter the new key in the Web UI. Recheck 401/200. Rotation is documented, **not yet performed**.
- Do not publicly expose port 8080. Any future `ai.marin.cr` design should include **TLS, network restrictions/Tailscale ACLs, and credential handling/rotation**, in addition to native llama.cpp authorization.
- Protect files and backups containing keys (`/etc/llama-qwen/api-key`, `~/.config/opencode/llama-api-key`); keep actual values out of documents, shell transcripts shared with others, repositories and logs.

## 20. Long-context scaling on Strix Halo — 64K baseline → 96K → 128K (research and planned tests, October 13, 2026)

> **Status: research-backed test plan, NOT a completed 96K/128K experiment.** The only context size conclusively tested on this machine is **65,536 tokens** (`/props` returned `65536`). The operator reports OpenCode is working reliably for extensive **server-configuration and administration tasks**, though the model is slow. No 96K or 128K benchmark output has yet been provided. Do **not** update the at-a-glance configuration to 128K until the host proves it works.
>
> **Do not change several variables at once.** Preserve the known-good GGUF, draft GGUF, Vulkan backend, MTP depth 2, GPU layers, `--parallel 1`, API-key credential handling, and kernel/GTT/UMA settings while measuring context size. Tests should run without OpenCode traffic.

### 20.1 Research conclusion for this hardware

**Recommendation:** test **98,304 tokens (96K)** first, then **131,072 (128K)**. This is a reasonable experiment for a Ryzen AI Max+ 395 with 128 GB unified RAM and Qwen3.8-27B Q4_K_M, **not a guarantee of useful 128K performance**. Qwen3.8-27B has a **262,144-token native context** in the model family documented by community benchmarks; our 64K limit was selected for safe bring-up, not a model architectural ceiling ([Strix Halo benchmark](https://github.com/yandaq/qwen3.8-27b-strix-halo/blob/main/benchmark.md); [llama.cpp 2026 discussion](https://github.com/ggml-org/llama.cpp/discussions/27154)).

| Tier | `-c` / server `n_ctx` | Suggested cold-prompt tests | Usage / status |
| --- | ---: | --- | --- |
| Known-good **64K** | **65536** | 2K, 30K, 60K | **Verified configuration**; full 60K benchmark **not yet run** |
| Intermediate **96K** | **98304** | 2K, 60K, 90K | **Planned** stability and speed checkpoint |
| Target **128K** | **131072** | 2K, 60K, 90K, 110K, 120K | **Planned**; tests must pass before OpenCode is changed |

For an OpenCode model setting of `"context": 131072` with `"output": 8192`, the *arithmetic* remaining space is 122,880 tokens. That is **not** a guaranteed 122,880-token usable repository payload: system instructions, chat-template tokens, tool schemas/results, thinking, generation, agent compaction, and provider-specific limits also consume budget. Keep headroom. The `limit` fields inform OpenCode's context accounting; they do not resize llama.cpp ([OpenCode provider docs](https://docs.opencode.ai/docs/providers/)).

**The limiting factor may be time or stability, not nominal RAM.** An extended context reserves/uses KV state and temporary buffers; actual prompt processing can slow nonlinearly as the position gets deeper. This system had about **2 GiB dedicated UMA VRAM**, ~**61 GiB reported GTT limit**, and ~**121 GiB Linux-usable RAM** at bring-up. Those represent different memory accounting domains; they must **not** be added as independent physical RAM. Do not immediately reserve 96 GiB of UMA or disable IOMMU.

### 20.2 Evidence, caveats, and relevant outside experiments

1. **Backend-sensitive long-context prefill:** one [`llama.cpp` gfx1151 discussion](https://github.com/ggml-org/llama.cpp/discussions/27154) measured Qwen3.8-27B on Strix Halo with ROCm and Vulkan and reported deteriorating *cold* prompt ingestion deeper into long contexts, plus less reliable retrieval at late positions. Its 96 GB test system/configuration is **not identical to our 128 GB box**. The author's figures are community tests, not assured results.
2. **Vulkan 128K allocation cliff:** [llama.cpp issue #27734](https://github.com/ggml-org/llama.cpp/issues/27734) documents a case where **raising allocated context from 98,304 to 131,072 sharply reduced token generation, even on a short prompt**. It was on a Radeon RX 7900 XTX/Windows/Vulkan, not our Strix Halo. The reported workaround is the runtime environment variable `GGML_VK_SUBALLOCATION_BLOCK_SIZE=4294967296`. Use **only if our own short-prompt 128K test reproduces the symptom**; driver/backend versions may behave differently.
3. **Deep-prompt risks on Strix Halo:** a [comment in llama.cpp issue #27638](https://github.com/ggml-org/llama.cpp/issues/27638) reports extreme prefill slowdown and device-loss near ~128K on another Strix Halo Vulkan configuration (for example, a ~100K input approaching an hour). This is a **cautionary anecdote**, not a prediction that our build will fail.
4. **Conflicting ROCm/Vulkan winners:** the [yandaq Strix Halo benchmark](https://github.com/yandaq/qwen3.8-27b-strix-halo/blob/main/benchmark.md) reports faster MTP verification on Vulkan for its specific 4-bit model/build; the separate [llama.cpp gfx1151 discussion](https://github.com/ggml-org/llama.cpp/discussions/27154) reports ROCm advantages for some prefill/speculation tests. **Benchmark the same GGUF, prompt, quantization, attention/KV settings and driver set on our hardware** before declaring a winner.
5. **Official flags and caching semantics:** upstream [`llama-server` documentation](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md) defines `-c`, `-b`, `-ub`, `--cache-type-k`, `--cache-type-v`, flash attention and cache reuse. These flags can evolve; always compare against this installed binary's `llama-server --help`.

### 20.3 Freeze a reproducible baseline BEFORE editing systemd

**Run while the current 64K service works**; keep key files out of Git and logs. Pause OpenCode and any long chat requests. Capture the deployed command, git commit and driver/kernel details:

```bash
mkdir -p ~/context-bench-results
chmod 700 ~/context-bench-results
sudo cp -a /etc/systemd/system/llama-qwen.service \
  /etc/systemd/system/llama-qwen.service.bak-64k
systemctl is-enabled llama-qwen.service
systemctl is-active llama-qwen.service
sudo systemctl cat llama-qwen.service \
  > ~/context-bench-results/systemd-unit-64k.txt
git -C ~/llama.cpp rev-parse HEAD | tee ~/context-bench-results/llama-commit.txt
uname -r | tee ~/context-bench-results/kernel.txt
vulkaninfo --summary 2>&1 | grep -E 'deviceName|driverName|driverInfo'
~/llama.cpp/build-vulkan/bin/llama-server --help 2>&1 \
  | grep -E 'ctx-size|cache-type-[kv]|flash-attn|batch-size|ubatch-size|cache-reuse'
```

The `systemd-unit-64k.txt` output should contain **only the credential file path**, never the API-key bytes. As always, inspect artifacts before publishing. Optionally record package/BIOS information using the host inventory in Section 1A.

**Context verification** (the `/props` GET was observed working locally without an explicit Bearer header; access rules may differ by llama.cpp version):

```bash
curl -fsS http://127.0.0.1:8080/props | \
  python3 -c 'import json,sys; print(json.load(sys.stdin)["default_generation_settings"]["n_ctx"])'
# Known-good recorded value: 65536
```

### 20.4 Benchmark harness: cold ingestion, retrieval accuracy and generation

Use the accompanying **`llama_context_bench.py`** (complete source archived below in Section 20.11). It uses Python's standard library, the existing **`~/.config/opencode/llama-api-key`** file, and authenticated llama.cpp endpoints; it does **not** reveal the API key. It creates synthetic server-config lines plus three planted unique checkpoint codes at approximately 10%, 50% and 90% of the input. Then it asks Qwen to reproduce all three exactly.

**Important test mechanics:**

- Counts raw payload tokens with llama.cpp `/tokenize` (there is additional chat-template overhead). It leaves margin for output; it **does not** measure exact post-template context occupancy.
- Sends `cache_prompt: false` for a **cold** request; results intentionally exclude OpenCode's normal prefix-cache benefits. This is a worst-case input-processing/stability test, **not a representative everyday agent throughput number**.
- Saves JSONL with input size, wall time, server `prompt_per_second` / `predicted_per_second`, finish reason, correct/incorrect early/middle/late recall, and before/after memory snapshots. It does **not** store the key or full synthetic prompt. Its default 1024 output-token budget can be overridden if reasoning exhausts the output before the final JSON.
- Each run can be slow and memory-intensive. **Run a small target first.** Stop on device-loss, repeated OOMs, swap thrashing or unexpectedly severe slowdowns; do not leave a large benchmark running unattended.
- Unlike normal OpenCode conversations, each prompt is newly synthesized. Repeat 3 warm/controlled trials where feasible. For clean engine comparisons, treat variance, system temperature, power policy, and model version as controls.

Save the companion Python source on the MINISFORUM as `~/llama_context_bench.py` (download/link was provided separately; the complete code is included below for standalone reproducibility). Basic check:

```bash
python3 -m py_compile ~/llama_context_bench.py
python3 ~/llama_context_bench.py --help
```

**Baseline while context is 65536:**

```bash
python3 ~/llama_context_bench.py --targets 2000 30000 60000
```

**96K context tests:**

```bash
python3 ~/llama_context_bench.py --targets 2000 60000 90000
```

**128K context tests (gradually; don't start with 120K):**

```bash
python3 ~/llama_context_bench.py --targets 2000 60000 90000
python3 ~/llama_context_bench.py --targets 110000 120000
```

If answers are truncated due to reasoning, repeat that test with an increased budget, e.g. `--max-tokens 2048`, and label the run separately. A longer output reserve means less usable input in the same context window.

Results save to `~/context-bench-results/context-results-YYYYMMDD-HHMMSS.jsonl`. Archive each file with a label for the *actual* context, inference version and backend; do not overwrite baseline files.

### 20.5 Change ONE server value at a time; verify after each restart

1. Edit the live service: `sudo nano /etc/systemd/system/llama-qwen.service`.
2. Modify **only** the existing `-c` setting (keep the trailing `\` continuation in the systemd file): `-c 98304 \` for 96K, then, only after success, `-c 131072 \` for 128K.
3. Keep `LoadCredential=llama-api-key:/etc/llama-qwen/api-key` and the final `--api-key-file %d/llama-api-key` **unchanged**. Preserve `--parallel 1`, `-ngl 99`, `-md ...`, `--spec-type draft-mtp`, `--spec-draft-n-max 2` and `--host 127.0.0.1`.
4. Apply and check:

```bash
sudo systemd-analyze verify /etc/systemd/system/llama-qwen.service
sudo systemctl daemon-reload
sudo systemctl restart llama-qwen.service
systemctl status llama-qwen.service --no-pager
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/props | \
  python3 -c 'import json,sys; print(json.load(sys.stdin)["default_generation_settings"]["n_ctx"])'
```

`/health` should return `{"status":"ok"}` once loaded (it is intentionally unauthenticated); `/props` should display `98304` or `131072` as configured. **A successful process start or `/props` alone does not prove high-load stability.** Run the benchmark before promoting the configuration to OpenCode.

### 20.6 Observe system memory, GPU allocation, temperatures and errors

Run separately in another SSH session while tests execute. On the verified host the AMD GPU was under `/sys/class/drm/card1`, but card numbering may change; the loop below detects readable AMD DRM memory counters on any card:

```bash
watch -n 2 '
free -h
printf "\nSystemd service memory:\n"
systemctl show llama-qwen.service -p MemoryCurrent
printf "\nGPU allocated memory:\n"
for f in /sys/class/drm/card*/device/mem_info_{vram,gtt}_used; do
  if [ -r "$f" ]; then printf "%s: " "$f"; numfmt --to=iec < "$f"; fi
done
'
```

Additional monitoring commands:

```bash
nvtop                     # GPU busy %, clocks, memory if supported
btop                      # CPU and RAM
watch -n 2 sensors        # thermals; exact sensor names vary
sudo journalctl -u llama-qwen -f
sudo journalctl -k -f | grep -Ei 'amdgpu|oom|gpu reset|device lost|fault'
```

Monitor **MemAvailable**, actual **SwapUsed/SwapFree**, GPU GTT/VRAM, systemd `MemoryCurrent`, prompt throughput by prompt depth, token-generation throughput, and GPU errors. 98% GPU busy *alone* does not establish efficient long-context use. Kernel/module errors are particularly important at 90K–120K.

### 20.7 Acceptance criteria for promoting 128K to OpenCode

These are **local operator acceptance targets, not hardware guarantees**:

| Criterion | Proposed gate |
| --- | --- |
| Server loads | `/health` ready, `/props` exact context, both GGUFs loaded, no repeated restarts |
| API security | Unauthenticated chat gives HTTP **401**, authenticated requests work; localhost bind unchanged |
| **Short-prompt regression** | 2K-prompt generation remains near the proven ~20–25 tok/s band; investigate repeated results **<15 tok/s** |
| **Deep-context usability** | 90K and 110K finish in a tolerable time; assess 120K only after the earlier tests pass |
| **Correctness** | All three positional checkpoint strings reproduced exactly, no omission/hallucination |
| Memory/thermal health | No sustained swap thrash, GPU reset, Vulkan device loss or OOM |
| Agent usefulness | OpenCode handles actual server administration tasks without repeated context truncations; record full wall-clock and tool count |

For each tier measure a **short cold prompt** (tests effects of allocation) **and** a genuinely populated near-limit prompt (tests deep-context compute/quality). No single tok/s benchmark is enough. A 128K server that loads but takes tens of minutes to read a fresh 100K prompt is **not a practical win** for interactive use.

### 20.8 Optimization ladder, ordered by operational risk

**Tier 0 — cheapest productivity improvements (do before driver or memory tuning):**

- Use OpenCode's agent/session compaction responsibly; keep a concise structured context file/`AGENTS.md` for stable architecture, constraints and prior decisions rather than replaying lengthy shell transcripts. **Review any summarization for lost requirements**.
- Limit command output (`journalctl -n`, targeted `grep`, `sed`, file-specific reads); avoid dumping binary files, all server logs, or secrets into the agent prompt. Store large artifacts as files and supply precise excerpts.
- Preserve cacheable prompt prefixes; OpenCode reported many cache-read tokens, but confirm actual llama.cpp prompt-cache savings from the server logs. llama.cpp supports `--cache-prompt` (normally on) and optional `--cache-reuse`; **test their impact before adjusting** ([upstream docs](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)).

**Tier 1 — low/moderate-risk llama.cpp flags; test one at a time after the 128K baseline:**

- Keep `--parallel 1` for a single coding agent initially; raising slot concurrency can increase cache pressure and alter per-request usable context depending on KV-pool configuration.
- Keep `--flash-attn auto` (verified working baseline). Test `--flash-attn on` only with validated support/perf, not as a blind fix.
- Test more compact KV formats **only if memory is a limiting factor**, e.g. `--cache-type-k q8_0 --cache-type-v q8_0` against the default f16, with retrieval-quality/precision regression tests. Lower-bit `q4_0` is a subsequent, more aggressive option. Qwen's hybrid attention means memory scaling may be unlike a conventional full-attention 27B model; measure **actual** allocations. KV compression can improve fit yet harm quality or throughput on some backends.
- Compare `-b` and `-ub` only if cold prompt-processing is the constraint; changes can affect allocation and throughput. Record both values. Do not tune draft depth, quantization and cache dtype at the same time.

**Tier 2 — conditional Vulkan allocation issue:**

If a 2K-token test runs at ~20–25 tok/s with `-c 98304` but suddenly collapses at `-c 131072` **even though the input remains tiny**, test the **reported** Vulkan suballocation setting from [issue #27734](https://github.com/ggml-org/llama.cpp/issues/27734) using a reversible systemd drop-in (this workaround was reported on a **different AMD GPU**, not verified here):

```bash
sudo systemctl edit llama-qwen.service
# Enter only these lines in the editor:
# [Service]
# Environment=GGML_VK_SUBALLOCATION_BLOCK_SIZE=4294967296
# Save the file and close the editor.
sudo systemctl daemon-reload
sudo systemctl restart llama-qwen.service
systemctl show llama-qwen.service -p Environment
```

The actual drop-in content must **not** be commented; the three `#` lines above are instructions, not content to paste. The drop-in should contain:

```ini
[Service]
Environment=GGML_VK_SUBALLOCATION_BLOCK_SIZE=4294967296
```

Compare with and without on **identical short and long prompts**. Check the installed Vulkan backend honors the environment variable. Undo with `sudo systemctl revert llama-qwen.service` **only if there are no other valuable unit overrides**; otherwise remove just the created drop-in and reload. This setting changes Vulkan allocation behavior, not the actual context limit.

**Tier 3 — advanced changes after baseline evidence:**

- Build llama.cpp with **ROCm/HIP** in a **separate** build directory alongside `build-vulkan`; compare using the same main/draft GGUF, actual context occupancy, temperature/power profile, and MTP behavior. The upstream community measurements conflict; **ROCm is not automatically faster** for Qwen3.8+MTP on gfx1151 ([benchmark A](https://github.com/yandaq/qwen3.8-27b-strix-halo/blob/main/benchmark.md); [benchmark B](https://github.com/ggml-org/llama.cpp/discussions/27154)). Do not replace a working driver or remove the Vulkan binary to run this comparison.
- Consider changing **GTT** allocation **only after** logs/metrics prove it is the limiting resource. The current ~61 GiB GTT limit is a kernel-addressable limit, not a simple interchangeable 61 GiB discrete VRAM card. Any kernel parameter change needs a documented rollback and fresh reboot validation; **do not disable IOMMU** as a generic performance shortcut.
- Evaluate power/thermal modes separately (`powerprofilesctl get`, `powerprofilesctl list`, `sensors`); sustained thermals can matter at 100K+ even when 2K benchmarks look excellent.

### 20.9 Promote OpenCode only after 128K passes

Stop existing OpenCode sessions, then edit `~/.config/opencode/opencode.json` and modify **only** the `limit` object for `provider.local-llama.models.qwen3.8-27b`. Preserve the protected key reference, endpoint, provider ID and output budget:

```json
"limit": {
  "context": 131072,
  "output": 8192
}
```

Keep `"apiKey": "{file:~/.config/opencode/llama-api-key}"` and `"baseURL": "http://127.0.0.1:8080/v1"` exactly as already tested. If 96K proves the best operational compromise, set OpenCode to **98304**, not 131072. Never advertise a larger context than the running model server actually supports.

**Repeat a representative server-configuration task** using a scratch copy of a non-sensitive test repository. A suggested comparable agent prompt is:

> Inspect the configuration files in this test repository. Identify the service units, reverse-proxy configuration, and deployment dependencies. Make a concise system map, propose an improvement with rollback steps, implement it only in this sandbox, run available validation commands, and summarize every file changed. Do not print or modify credentials.

Record session ID, exact prompt, start/end clock time (`date -Is`), task correctness, actual files/commands used, failures/retries, OpenCode usage stats, server journal timings, context occupancy and whether the agent compacted. **This prompt is a proposed future test, not an already executed one.**

### 20.10 Recovery: restore the known-good 64K service

If 96K/128K is unstable or becomes unacceptably slow:

```bash
sudo cp -a /etc/systemd/system/llama-qwen.service.bak-64k \
  /etc/systemd/system/llama-qwen.service
sudo systemctl daemon-reload
sudo systemctl restart llama-qwen.service
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/props | \
  python3 -c 'import json,sys; print(json.load(sys.stdin)["default_generation_settings"]["n_ctx"])'
```

Restore `"context": 65536` in OpenCode if it had been raised. If a special Vulkan suballocation drop-in was added, remove **only that specific override** and reload. Rollback should preserve the API key, MTP, GPU permissions, Cockpit and boot behavior.

### 20.11 Full benchmark program for reproducibility

The following source was supplied as the companion `llama_context_bench.py` for this test plan. Check it against your installed `llama-server` API if you update llama.cpp significantly. **Do not execute it during an active OpenCode task.**

```python
#!/usr/bin/env python3
"""Long-context llama.cpp benchmark using synthetic server configuration records.

Uses only Python's standard library. Reads a local API key without printing it.
Tests cold prompt processing, recall at different prompt positions, generation speed,
and memory. Do not run during an active OpenCode session.

Example:
  python3 llama_context_bench.py --targets 2000 30000 60000
  python3 llama_context_bench.py --targets 90000 110000 120000
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import time
import urllib.error
import urllib.request

DEFAULT_URL = "http://127.0.0.1:8080"


def post_json(url, key, payload, timeout=120):
    req = urllib.request.Request(url, json.dumps(payload).encode("utf-8"), {
        "Authorization": f"Bearer {key}",
        "Content-Type": "application/json",
    })
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def get_json(url, key, timeout=60):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {key}"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def memory_gib():
    result = {}
    paths = {
        "gpu_vram_used_gib": "/sys/class/drm/card1/device/mem_info_vram_used",
        "gpu_gtt_used_gib": "/sys/class/drm/card1/device/mem_info_gtt_used",
    }
    for name, path in paths.items():
        try:
            result[name] = round(int(Path(path).read_text().strip()) / (1024 ** 3), 2)
        except (OSError, ValueError):
            pass
    try:
        mem = {}
        for line in Path("/proc/meminfo").read_text().splitlines():
            k, _, v = line.partition(":")
            if k in ("MemAvailable", "SwapFree", "SwapTotal"):
                mem[k] = round(int(v.strip().split()[0]) / (1024 ** 2), 2)
        result["system_memory_gib"] = mem
    except OSError:
        pass
    return result


def code_line(n):
    return (f"service_record_{n:06d}: environment=prod region=us-east-{n % 3 + 1} "
            f"component=worker_{n % 97:02d} timeout_ms={1000 + n % 6000} "
            f"max_retries={n % 8} cache_enabled={'true' if n % 2 else 'false'} "
            f"release_tag=v{1+n%7}.{n%23}.{n%17} audit_key=A{(n * 7919) % 100000:05d}\n")


def generate_records(count, target):
    values = {}
    positions = {"EARLY": count // 10, "MIDDLE": count // 2, "LATE": count * 9 // 10}
    out = []
    for i in range(count):
        for name, pos in positions.items():
            if i == pos:
                value = hashlib.sha256(f"benchmark-{target}-{name}-2026".encode()).hexdigest()[:12].upper()
                values[name] = f"TRACE-{name}-{value}"
                out.append(f"# CRITICAL CHECKPOINT {name}: confirmation_code={values[name]}\n")
        out.append(code_line(i))
    return "".join(out), values


def count_tokens(base, key, content):
    obj = post_json(base + "/tokenize", key, {"content": content, "add_special": False}, timeout=180)
    return len(obj["tokens"])


def build_prompt(base, key, requested):
    intro = (
        "You are auditing synthetic production server configurations for a test. "
        "Only the three lines beginning '# CRITICAL CHECKPOINT' matter. "
        "All other lines are distractors. Find their exact confirmation_code values. "
        "Do not infer or invent any values.\n\n"
    )
    question = (
        "\n\nReturn exactly one JSON object with keys EARLY, MIDDLE, LATE, "
        "each containing the corresponding confirmation_code copied exactly from "
        "the CRITICAL CHECKPOINT lines. No other text."
    )
    n = max(20, requested // 44)
    # Fit target approximately. Calls tokenize a few times using actual model tokenization.
    for _ in range(5):
        records, values = generate_records(n, requested)
        prompt = intro + records + question
        actual = count_tokens(base, key, prompt)
        delta = requested - actual
        if abs(delta) <= max(80, requested // 500):
            break
        adjusted = max(20, int(n * requested / max(1, actual)))
        n = adjusted if adjusted != n else max(20, n + (1 if delta > 0 else -1))
    return prompt, values, actual


def main():
    parser = argparse.ArgumentParser(description="Long-context test for local llama.cpp")
    parser.add_argument("--targets", type=int, nargs="+", default=[2000, 30000, 60000], help="approximate prompt tokens per test")
    parser.add_argument("--base-url", default=DEFAULT_URL)
    parser.add_argument("--model", default="qwen3.8-27b")
    parser.add_argument("--key-file", default="~/.config/opencode/llama-api-key")
    parser.add_argument("--out-dir", default="~/context-bench-results")
    parser.add_argument("--max-tokens", type=int, default=1024, help="output budget for reasoning and final answer")
    parser.add_argument("--timeout", type=int, default=3600, help="max seconds per generation request")
    args = parser.parse_args()
    keyfile = Path(args.key_file).expanduser()
    try:
        key = keyfile.read_text().strip()
    except OSError as e:
        parser.error(f"Cannot read API key file {keyfile}: {e}")
    if not key:
        parser.error("API key file is empty")
    base = args.base_url.rstrip("/")
    folder = Path(args.out_dir).expanduser()
    folder.mkdir(parents=True, exist_ok=True)
    try:
        folder.chmod(0o700)
    except OSError:
        pass
    try:
        props = get_json(base + "/props", key)
        context = props["default_generation_settings"]["n_ctx"]
    except Exception as e:
        parser.error(f"Could not inspect server context: {e}")
    print(f"Server context: {context} tokens; model: {args.model}")
    print("These are synthetic data, not real files or credentials. Pause OpenCode while benchmarking.")
    output_file = folder / f"context-results-{dt.datetime.now().strftime('%Y%m%d-%H%M%S')}.jsonl"
    for target in args.targets:
        if target + args.max_tokens + 300 > context:
            print(f"SKIP {target}: exceeds context {context} with output margin {args.max_tokens}+300")
            continue
        print(f"\nPreparing ~{target:,} input tokens ...", flush=True)
        started = dt.datetime.now(dt.timezone.utc).isoformat()
        entry = {"timestamp": started, "target_tokens": target, "n_ctx": context, "model": args.model}
        try:
            prompt, values, n_tokens = build_prompt(base, key, target)
            entry["text_tokens_pre_chat_template"] = n_tokens
            entry["memory_before"] = memory_gib()
            payload = {
                "model": args.model,
                "messages": [{"role": "user", "content": prompt}],
                "max_tokens": args.max_tokens,
                "temperature": 0,
                "stream": False,
                "cache_prompt": False,
            }
            print(f"Running generation ({n_tokens:,} tokenizer-count tokens + chat overhead) ...", flush=True)
            tic = time.monotonic()
            response = post_json(base + "/v1/chat/completions", key, payload, args.timeout)
            entry["wall_seconds"] = round(time.monotonic() - tic, 2)
            entry["memory_after"] = memory_gib()
            entry["usage"] = response.get("usage")
            entry["timings"] = response.get("timings")
            choice = response["choices"][0]
            entry["finish_reason"] = choice.get("finish_reason")
            message = choice["message"]
            answer = message.get("content") or ""
            entry["answer_preview"] = answer[:500]
            entry["recall_pass"] = {k: (val in answer) for k, val in values.items()}
            entry["all_recalled"] = all(entry["recall_pass"].values())
            print("Result:", {"tokens": n_tokens,
                              "wall_s": entry["wall_seconds"],
                              "prompt_tps": round(entry["timings"].get("prompt_per_second", 0), 1) if entry["timings"] else None,
                              "decode_tps": round(entry["timings"].get("predicted_per_second", 0), 1) if entry["timings"] else None,
                              "finish": entry["finish_reason"],
                              "recall": entry["recall_pass"]}, flush=True)
        except Exception as e:
            entry["error"] = str(e)
            print("ERROR:", e, flush=True)
        with output_file.open("a", encoding="utf-8") as f:
            f.write(json.dumps(entry, ensure_ascii=False) + "\n")
        os.chmod(output_file, 0o600)
    print(f"\nSaved results: {output_file}")


if __name__ == "__main__":
    main()
```

### 20.12 Decision log — complete after each test

| Date/time | llama.cpp commit | Backend/driver | `n_ctx` | KV dtype | 2K tok/s | 60K prefill tok/s | 90K/120K prefill | Recall E/M/L | Peak RAM/GTT | Error/decision |
| --- | --- | --- | ---: | --- | ---: | ---: | --- | --- | --- | --- |
| Oct 12–13, 2026 | not yet captured | Vulkan RADV 26.0.8 at bring-up | **65536** | default, believed f16 (confirm) | earlier short chat ~22–24 | **not measured** | n/a | not measured | 2 GiB UMA, ~61 GiB GTT limit | **Keep as known-good service context** |
| Future | — | — | 98304 | — | — | — | 90K: — | — | — | **Pending** |
| Future | — | — | 131072 | — | — | — | 90K/120K: — | — | — | **Pending** |

The ~22–24 tok/s figure is from prior **different chat workloads**, **not** a controlled 2K context benchmark. Keep those facts separate. Add a new dated line for every repeatable result and keep raw JSONL files and journal excerpts with the runbook.

## 21. Rebuild/recovery checklist (quick reference)

This is the **shortest reproduction path from an already working Ubuntu host**. No OS installation steps are included.

1. Confirm the AMD **gfx1151**/Radeon 8060S shows in `rocminfo` and `vulkaninfo`; inspect GPU device files and current 2 GiB UMA / ~61 GiB GTT baseline.
2. Ensure `marinoscar` belongs to `render,video`; reconnect after `usermod` if necessary.
3. Install `pipx`/`huggingface_hub`; download the main GGUF **and** `mtp-Qwen3.8-27B-Q4_0.gguf` into `~/models/qwen3.8-27b`.
4. Clone and build `ggml-org/llama.cpp` using Vulkan (`-DGGML_VULKAN=ON`). Preserve the exact commit/version for reproducibility.
5. (Optional baseline) Run the model without MTP at `127.0.0.1:8080`, request full GPU offload (`-ngl 99`), context 8192; check correctness, server logs and timings.
6. Generate the root-only key as in **Section 19**, then create the **authenticated** `/etc/systemd/system/llama-qwen.service` from Section 10; preserve MTP, 64K context, `LoadCredential`, and `--api-key-file`.
7. Run `sudo systemctl daemon-reload && sudo systemctl start llama-qwen`; wait for `model loaded` and `listening`.
8. Check logs for `loading draft model` and `draft acceptance`; send the two-weighing nine-coin reasoning test and confirm `finish_reason: stop`.
9. Install and start `cockpit.socket`; enable admin access in the Web UI, then find **Services → llama-qwen.service** for start/stop/restart/logs even with auto-start enabled.
10. Open `http://127.0.0.1:8080` on the AI workstation, authenticate with the API key, test chat and reasoning; or use a secure tunnel from another PC with the same API-key requirement.
11. For the requested always-on behavior, run `sudo systemctl enable --now llama-qwen.service` and verify `systemctl is-enabled llama-qwen.service` → `enabled`; test one reboot and check `/health`. To revert, `sudo systemctl disable --now llama-qwen.service`. Keep the authenticated API **bound to 127.0.0.1** until a protected remote-access design is deployed.
12. Confirm current 64K context by `curl -s http://127.0.0.1:8080/props`; `/props` returned `65536` in this session.
13. Create the OpenCode key file as in Section 19 and install/configure OpenCode as in Section 12; run the **exact calculator prompt** in Section 13 and collect `opencode stats` + journal logs as in Section 14.
14. Check **401 without** and **200 with** the Bearer key on `/v1/chat/completions`; verify authenticated OpenCode and Web UI. Capture git commit, GGUF checksums, logs and matched benchmarks before changing other settings.

**Minimal smoke-check commands:**

```bash
systemctl is-active llama-qwen
systemctl is-enabled llama-qwen
curl -sS http://127.0.0.1:8080/health
sudo journalctl -u llama-qwen -n 80 --no-pager
```

## 22. Open items / next experiments

- [ ] Record the exact current llama.cpp Git commit, build flags, executable version, and both model SHA-256 checksums.
- [ ] Capture the full model-load/offload logs: verify exact GPU selection and main/draft layer counts rather than inferring offload from `-ngl`.
- [x] Verify the MTP draft GGUF exists locally (1.6G as `ls -lh` reports).
- [x] Confirm the deployed systemd launch flags and observe draft-model loading.
- [x] Observe MTP acceptance in server journal: 30/46 accepted (65.2%); mean length 2.30 on one request.
- [x] Set up a persistent systemd service and access it through Cockpit (initially on-demand).
- [ ] Execute `sudo systemctl enable --now llama-qwen.service` and confirm enabled state; perform successful post-reboot `/health` test (commands documented; not yet confirmed).
- [x] Verify llama.cpp built-in Web UI and reasoning-enabled chat response.
- [ ] Verify the *exact process flags* during the earlier 24.15 tok/s nine-coin benchmark (were not captured at the time).
- [ ] Run MTP on/off with matched prompts/settings and multiple warm runs, then compare draft depths 2/3/4.
- [ ] Benchmark ROCm against Vulkan RADV for the same GGUF under matched conditions.
- [x] Confirm effective server context `65536` with `/props`; completed one OpenCode coding session.
- [ ] Measure quality and latency at 16K–64K *actually populated* practical contexts, KV/cache memory pressure and precise end-to-end latency.
- [ ] Execute Section 20 cold 64K baseline, then 96K/128K synthetic recall benchmarks, record memory/latency and failure thresholds; do not promote OpenCode until 128K passes.
- [ ] Compare conditional Vulkan suballocation behavior at 131072, only if a short-prompt allocation cliff appears; evaluate q8_0 KV and ROCm with separately controlled trials.
- [ ] Decide whether GTT tuning is needed for larger models based on actual measurements.
- [x] Enable native API-key protection; confirm HTTP 401/200 on chat completions; verify OpenCode and Web UI authentication.
- [ ] Implement secure **remote** access with Tailscale/SSH or an authenticated HTTPS gateway, firewall and ACLs before external exposure.
- [x] Install and use OpenCode for a real calculator coding-agent task; inspect `opencode stats`, model logs, MTP acceptance and live GPU utilization.
- [ ] Export the specific OpenCode session, record its true start/end timestamps, test the calculator functional edge cases, and time repeated fresh runs.
- [ ] Consider Open WebUI only if the built-in llama.cpp interface lacks needed chat-history/files/model-management features; not installed yet.

## 23. Reference links

- [MINISFORUM MS-S1 Max — published product specifications](https://store.minisforum.com/products/minisforum-ms-s1-max-mini-pc)
- [AMD Ryzen AI Max+ 395 — CPU/GPU/memory specifications](https://www.amd.com/en/products/processors/laptop/ryzen/ai-300-series/amd-ryzen-ai-max-plus-395.html)
- [AMD ROCm GPU reference (gfx1151)](https://rocm.docs.amd.com/en/latest/reference/gpu-specs.html)
- [Ubuntu 26.04 LTS release announcement](https://canonical.com/blog/canonical-releases-ubuntu-26-04-lts-resolute-raccoon)

- [llama.cpp source](https://github.com/ggml-org/llama.cpp)
- [llama.cpp server documentation](https://github.com/ggml-org/llama.cpp/tree/master/tools/server)
- [Hugging Face model repository — Qwen3.8-27B GGUF](https://huggingface.co/ggml-org/Qwen3.8-27B-GGUF)
- [Hugging Face CLI documentation](https://huggingface.co/docs/huggingface_hub/guides/cli)
- [AI Toolbox Cockpit (optional inference management CLI, distinct from Cockpit)](https://github.com/kyuz0/ai-toolbox-cockpit)
- [Cockpit Project — Linux server administration](https://cockpit-project.org/)
- [systemd.service manual](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html)
- [systemd.exec credentials documentation](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
- [OpenCode — official site and installation](https://opencode.ai/)
- [OpenCode — configuration](https://opencode.ai/docs/config/)
- [OpenCode — models and providers](https://opencode.ai/docs/models/)
- [nvtop — AMD/NVIDIA/Intel GPU monitor](https://github.com/Syllo/nvtop)
- [Qwen3.8-27B / Strix Halo reproducible 4-bit benchmark (yandaq)](https://github.com/yandaq/qwen3.8-27b-strix-halo/blob/main/benchmark.md)
- [llama.cpp community gfx1151 / long-context discussion #27154](https://github.com/ggml-org/llama.cpp/discussions/27154)
- [llama.cpp Vulkan 131072 context suballocation issue #27734](https://github.com/ggml-org/llama.cpp/issues/27734)
- [llama.cpp deep-context Vulkan slowdown / GPU reset reports #27638](https://github.com/ggml-org/llama.cpp/issues/27638)
- [OpenCode provider configuration and context limits](https://docs.opencode.ai/docs/providers/)


---

**Bottom line (v3.3):** On October 12, 2026, this MS-S1 Max ran Qwen3.8-27B Q4_K_M with a native Vulkan-enabled llama.cpp backend and an **MTP draft model demonstrably loaded and accepting tokens**. The reasoning test improved from **11.27** to **24.15 tok/s** between recorded runs (not a controlled A/B), and the later **systemd/Cockpit-backed Web UI** delivered **21.18 tok/s** server-reported on its short test (the browser showed ~21.59 tok/s). The AI server is controllable from a browser and persistent across SSH disconnections. **Automatic boot startup is documented but still lacks a captured `is-enabled` result and reboot health check**. The server was then verified at **65,536 tokens of context**, and OpenCode produced an iPhone-style `calculator.html` through real `edit`/`write`/`read`/`bash` tool calls. The coding-run log aggregate was **14 requests, 9,206 generated tokens, 22.30 tok/s weighted generation, 76.45% draft acceptance, and 7m37.95s of measured model processing**; **true end-to-end task time was not recorded**. The **chat inference API now requires a Bearer key** (verified 401/200); OpenCode and the built-in Web UI can use that credential. Binding is still localhost-only, with remote access not yet deployed.

**Long-context addendum (v3.3):** 96K and 128K are **test targets only**. The proposed staged research and full self-contained synthetic benchmark program are in Section 20; the verified deployed context remains **65,536 tokens** until new results are captured.
