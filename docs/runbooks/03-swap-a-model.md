# 03. Swap a model (change the "brain")

Use this when you want a node to run a different AI model, choose the model that starts at boot, download a model, or add a new model to the catalog.

> **Before you start**
> - You need: the node's model id (for example `qwen3.5-9b-q4km`). `aictl models` lists them.
> - For the cloud way: internet, the node in **online** mode, and the `lab:operate` permission on `https://ai.marin.cr`.
> - Deleting a model needs the **admin PIN** (or `sudo`).
> - **Time:** 1 to 5 minutes per swap. A download can take hours. Adding a model to the catalog: about an hour of work plus review.

> 🛠 Verify when implemented (Epic E2 router and catalog; E6 cloud for Part 1; E8 desktop for Part 2).

**Words used on this page**
- **Model** or **brain:** one AI. Each has an id from the **catalog**, the lab's list of approved models.
- **Loaded:** running in graphics memory, ready to answer. **Stored:** on disk, not running. **Absent:** not on this computer.
- **Graphics memory (VRAM):** the memory on the graphics card. A model must fit in it. On ai-n1 the graphics chip shares the computer's main memory ("unified memory").
- **Default model:** the model that starts by itself when the computer starts.

## What "doesn't fit" means
Every model in the catalog says how much graphics memory and normal memory (RAM) it needs. The node checks before loading:
- **Never fits** (`LAI-CAT-005`): the model needs more graphics memory than this computer has, or a backend it doesn't have (for example a CUDA-only model on a Vulkan computer). It is shown with ✗ and the reason, for example `✗ does not fit: needs 11.0 GB of graphics memory, this computer has 8.0 GB`. No setting changes that; choose a smaller model.
- **Doesn't fit right now** (`LAI-LLM-011`): it would fit, but other models are using the memory, or the node already runs as many models as it allows (`models_max`). Normally the node unloads the least recently used model to make room. If you asked it to keep the others, it refuses instead.
- **Files missing** (`LAI-LLM-008`): it fits but isn't downloaded. Download it (Part 3, step 9) or import it from the ark drive ([04](04-ark-drive.md)).

`aictl models --loadable` lists only the models that fit and are on disk.

---

## Part 1. From the cloud (ai.marin.cr)

1. Open `https://ai.marin.cr` → **Lab** → press **Open** on the node.
   - **What you should see:** the node page with tabs **Models**, **Ark**, **Services**, **Logs**, **Keys**, **Settings**. The Models table shows each model's **State**, **On disk** and **Fits?**.
   - **If it doesn't work:** the node shows "Offline mode" or "Last seen … ago": the cloud can't reach it. Use Part 2 or 3 on the node itself.
2. In the row of the model you want, press **Load**.
   - **What you should see:** "Running command: Load … 42 s". Within a few seconds to minutes the state becomes **loaded**. Another model may become unloaded to make room.
   - **If it doesn't work:** the command shows a code and its fix. `LAI-LLM-009` or `LAI-LLM-010`: the model failed or was too slow to start; the previous model was started again. See [07 → A model won't load](07-troubleshooting.md#a-model-wont-load).
3. To make it the model that starts at boot, press **Set default**. The ★ moves to it.
4. To download a model that isn't on the node, press **Download <size>**. Deleting files is in the row menu **⋮ → Delete files…** (lab admin; type the model id). The node never deletes the last verified copy of a model.

## Part 2. From the desktop app

5. Open the **Local AI** app. In **This computer**, open the models list.
6. Press **Load** next to the model. Use **Set default** to choose the boot model.
   - **What you should see:** a progress bar, then the model shows as loaded. The tray tooltip shows the model id.
   - **If it doesn't work:** the app shows the code and fix; see [07 → A model won't load](07-troubleshooting.md#a-model-wont-load).

In **Survival mode** the same thing is the Portal's button **8 Switch brain** ([01](01-survival-mode.md)).

## Part 3. From the terminal

7. **See what's there and what fits:**

   ```
   aictl models
   aictl models --loadable
   ```

   - **What you should see:** a table `MODEL  SIZE  STATUS  NOTES`, with `loaded`, `stored` or `absent`, and `✗ does not fit: …` where it doesn't fit.
   - `aictl models --all` shows the whole catalog.

8. **Load a model:**

   ```
   aictl load qwen3.5-9b-q4km
   ```

   - **What you should see:** `… Unloading <other model> to make room`, a progress bar, then `✓ Qwen 3.5 9B is ready. (7.1 s)` and the URL to use it.
   - To keep the others loaded (only if there is room): `aictl load <model-id> --keep-others`.
   - **If it doesn't work:** `✗ The model … failed to start. The previous model was started again. [LAI-LLM-009]`. Choose a smaller model or read `aictl logs --unit llama`. `LAI-AGT-022`: another load is running; wait and retry.

9. **Download a model** (needs internet and **online** mode):

   ```
   aictl download <model-id> --wait
   ```

   - **What you should see:** download progress until the file is downloaded and verified. Without `--wait`, follow it with `aictl ark status`.
   - **If it doesn't work:** `LAI-SYN-010`: refused in Survival mode. In Offline mode add `--allow-offline-mode`. With no internet, import from the ark drive instead ([04](04-ark-drive.md), `aictl ark import --item <model-id> --wait`).

10. **Choose the boot model:**

    ```
    aictl default qwen3.5-9b-q4km
    ```

    - **What you should see:** `✓ The default model is now Qwen 3.5 9B. It starts automatically when the computer starts.`

11. **Unload or delete:**

    ```
    aictl unload <model-id>
    aictl delete <model-id>
    ```

    - `delete` asks for confirmation and the PIN (or run it with `sudo`). It refuses to delete the last verified copy anywhere unless you add `--allow-last-copy` and type the model id again. Don't do that on an ark laptop.

---

## Part 4. Picking a model that fits

12. Start from `aictl models --loadable`. Every model listed there fits this computer.
13. Rules of thumb:
    - **8 GB graphics memory** (ai-n3, profile `small-8gb`): one model at a time, about 9B parameters at Q4, or a small vision model. Switching between them is normal.
    - **12 GB** (ai-n2, `cuda-12gb`): one bigger model (for example `qwen3.6-35b-a3b-q4km`, partly on the processor) or a 9B with room to spare.
    - **128 GB unified** (ai-n1, `max-128gb`): several big models at once (up to `models_max`).
    - **No usable graphics card** (`cpu-only`): only the tiny fallback model, and answers are slow.
14. **On battery** (laptops in an emergency) choose the smallest model; see [01 Part 4](01-survival-mode.md).
15. Your profile decides which models are downloaded automatically. To see it: `aictl profile show`. To see which profile fits this hardware: `aictl profile suggest`.

---

## Part 5. Add a new model to the catalog

The catalog (`catalog/models.yaml` in this repo) is the only list of models any node will download or load. Adding one is a pull request (PR) that someone else reviews. Schema and rules: [catalog.md §2](../specs/catalog.md#2-modelsyaml).

16. **Open an issue** in `marinoscar/local-ai` ("Add model `<id>` to the catalog"), under the catalog epic, per [AGENTS.md](../../AGENTS.md). Create a branch named `feat/<issue#>-add-<short-name>`.
17. **Choose an exact file.** On Hugging Face, pick the GGUF repo and the exact file (for example `…-Q4_K_M.gguf`, plus an `mmproj` file for vision models). Write down:
    - the repo name,
    - the **commit hash** (40 characters, from the repo's **Files** history; never `main`),
    - each file's name, size in bytes and **sha256** (Hugging Face shows it as the LFS "SHA256").
18. **Write the entry** in `catalog/models.yaml`, copying an existing entry: `id` (lowercase, for example `newmodel-7b-q4km`), `display_name`, a plain `summary` (and `summary_es`), `roles`, `files`, `requirements` (backends, `min_vram_mib`, `min_ram_mib`, `min_disk_bytes`) and the `llama` block (`ctx`, `n_gpu_layers`, …). For now put your best estimate in `min_vram_mib`; step 21 replaces it with a measured value. Add the id to the profiles that should carry it (`catalog/profiles/*.yaml`) and bump `catalog/CATALOG_VERSION` (format `YYYY.MM.DD-N`).
19. **Check it:**

    ```
    aictl catalog lint catalog/
    ```

    - **What you should see:** no problems,exit code 0.
    - **If it doesn't work:** each problem is a line like `catalog/models.yaml:models[3].source.revision  LAI-CAT-011  revision must be a 40-hex commit`. The codes `LAI-CAT-010` to `LAI-CAT-019` are the lint rules, one per rule. Fix it and run again (exit code 2 means problems were found).
20. **Put the draft on one test node** and get the files onto it. Copy your `catalog/` folder to the test node, then:

    ```
    sudo aictl catalog use catalog/ --test
    aictl download <new-id> --wait
    ```

    - **What you should see:** `` ✓ Test catalog <version> is in use (from ./catalog). Undo with `sudo aictl catalog restore`. ``, then the download progress. The node keeps the test catalog until you restore it or the next successful catalog refresh.
    - **If it doesn't work:** lint problems are printed as in step 19 and nothing is changed; fix them first. `LAI-CLI-006`: you forgot `sudo`.
21. **Measure graphics memory and speed:**

    ```
    aictl bench <new-id>
    ```

    It asks to unload the current models (they are put back at the end). It loads the model at its full context, sends a test request, measures peak graphics memory, then runs `llama-bench`.
    - **What you should see:** `graphics memory 6.6 GB at ctx 32768 → suggested min_vram_mib: 7240`, speeds in tokens per second, the result file in `/var/lib/localai/bench/`, and a ready YAML snippet "For the catalog PR".
    - **If it doesn't work:** `LAI-LLM-025` (benchmark failed): close other GPU programs and try again. If the result says "disturbed", someone else loaded a model during the run; run it again.
22. **Paste the measured `min_vram_mib`** (with the comment line the command printed) into your entry. If the model is meant for several node types, measure on each (8 GB, 12 GB, unified) and use the value from the node where it is tightest.
23. **Open the PR** with `Closes #<issue>`. In the description, include the bench output (GPU, build, date), the lint result, and a short test ("asked 3 questions, answers were sensible"). Run `scripts/check-redaction.sh` first.
24. **After merge:** the lab admin imports the new catalog release in the cloud (**Lab → Catalog → Import release…**), checks the lint, and presses **Approve** ([10](10-lab-operations.md)). Nodes pick it up on their next nightly sync, or at once with **Push to nodes**. Then restore the test node's catalog (`sudo aictl catalog restore` puts back the catalog it had before step 20, or let the next sync replace it).
