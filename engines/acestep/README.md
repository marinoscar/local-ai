# localai_music_shim

The small HTTP server that sits between the Local AI agent and ACE-Step (epic E11). The agent starts it
as `localai-music.service` when a music model is loaded and stops it on unload. Nobody else talks to it:
it listens on `127.0.0.1` only.

- Package: `localai_music_shim`, run as `python -m localai_music_shim`.
- Standard library only (`http.server`). ACE-Step and its dependencies come from the runtime venv
  (`/opt/localai/acestep/current/venv`), installed offline from the kit item `kit-engine-acestep-<backend>` (ADR-0015).
- Settings come from `/etc/localai/music.env`, which the agent writes: `MUSIC_PORT` (8082),
  `MUSIC_MODEL_ID`, `MUSIC_MODEL_DIR`, `MUSIC_WORK_DIR`, `MUSIC_DEVICE`, `MUSIC_DTYPE`,
  `MUSIC_MAX_QUEUE` (4). `MUSIC_BACKEND=fake` swaps ACE-Step for a fake that writes a short tone
  (tests, CI, plumbing checks on a node without a GPU).
- Names, paths and the protocol are owned by `docs/specs/music.md` and the canonical table of
  `docs/architecture/overview.md`; this file does not restate them beyond the summary below.

## Protocol (summary)

| Request | Answer |
|---|---|
| `GET /health` | `{"status": "loading"\|"ready"\|"error", "model_id", "device", "dtype", "vram_mib", "error"}` |
| `POST /v1/tasks` (JSON) | `202 {"task_id"}`, `429 {"error":"queue_full"}`, `400 {"error"}`, `503 {"error":"not_ready"}` while the model loads or failed |
| `GET /v1/tasks/{id}` | `{"state", "progress", "error", "outputs": [{"path", "duration_s", "sample_rate", "mime"}]}` |
| `POST /v1/tasks/{id}/cancel` | `200 {}` (`404` for an unknown task) |

One worker thread runs one task at a time; `MUSIC_MAX_QUEUE` counts the running task and the waiting ones. `sample_path` and
`output_dir` must be inside `MUSIC_WORK_DIR`. A failed or cancelled task leaves no files behind.

Privacy: lyrics stay in memory until the task ends and are never logged. Logs carry task ids, sizes and
counts only.

## ACE-Step

All engine calls are in `localai_music_shim/backend.py`: ACE-Step 1.5's Python API
(`acestep.inference.generate_music`), the turbo DiT with the 1.7B LM, both CPU offloads, a writable model
overlay in `MUSIC_WORK_DIR/_model`, and ACE-Step's own log kept free of lyrics. Why, and the measurements:
[`docs/source/acestep-spike.md`](../../docs/source/acestep-spike.md) (spike #505).

`MUSIC_BACKEND=acestep` (the default) is the real engine and needs the runtime venv and the weights in
`MUSIC_MODEL_DIR`; its unit tests (`tests/test_backend.py`) use a fake ACE-Step API and need neither.

## Tests

```
cd engines/acestep
python3 -m unittest discover -s tests -t .
```
