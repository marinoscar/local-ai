# 11. Load-testing the gateway

Use this before you rely on `https://ai.marin.cr/v1` for many people at once, and again after a change to the gateway's streaming code. It proves three things: the gateway adds almost no delay to each streamed word, it stays within memory, and it never fails when 200 streams run at the same time. The design is in [gateway.md §9](../specs/gateway.md#load-testing).

> **Before you start**
> - You need: a Linux or macOS computer with this repository, **Node.js 24**, and the cloud app's packages installed (`npm install` in `apps/cloud`). A database for the API (see the cloud app's README, "Run it").
> - **k6**, the load-testing program. It is a separate program, not part of this repository. Install it from [grafana.com/docs/k6/latest/set-up/install-k6](https://grafana.com/docs/k6/latest/set-up/install-k6/) (on macOS: `brew install k6`). You need version **1.2 or newer**. Check with `k6 version`.
> - The first time, k6 builds itself a copy with the streaming add-on (**xk6-sse**); that needs internet once and takes about a minute. On a computer with no internet, build it somewhere else with `xk6 build --with github.com/grafana/xk6-sse` and copy the `k6` file over.
> - **Time:** the fake test takes about 15 minutes in total; the real-lab test 20 minutes plus set-up.
> - **Never** run this from CI, and **never** switch the fake test on in a real deployment: the API refuses to start if you try (see "If it doesn't work").

---

## Part 1. The fake test (no GPU, no real node)

The API pretends there is one computer in the lab. That computer is a tiny program that answers every question with 300 words, one every 20 milliseconds. k6 opens up to **200 streams at once** for 5 minutes and measures, for every single word, how long the gateway held it.

Pass means all of these: the gateway adds **5 ms or less** per word (95 % of the time), the API uses **less than 600 MB** of memory, there are **0 errors**, and every stream finishes.

You need **four terminals**. Open them with **Ctrl + Alt + T**, and in each one go to the repository folder first (`cd` into it).

1. **Make a key for the test.** Open `https://ai.marin.cr` (or your own local copy of the cloud app) and go to **API keys → Create key**. Choose kind **Service**, no expiry, **concurrent 250** and **requests per minute 600 or more** (only a gateway administrator may set limits this high). Copy the key that is shown once and save it in a file only you can read:
   ```bash
   mkdir -p ~/.config/opencode && install -m 600 /dev/null ~/.config/opencode/localai-gateway-key
   # then paste the key into that file (nano ~/.config/opencode/localai-gateway-key)
   ```
   **What you should see:** the file exists and holds one line starting with `lai_`. Never paste the key into a chat, an issue or a file in the repository.

2. **Terminal 1: start the pretend computer.**
   ```bash
   node apps/cloud/apps/api/test/load/fake-upstream.mjs --port 18999
   ```
   **What you should see:** `fake-upstream listening on http://127.0.0.1:18999 (300 tokens, 20 ms apart)`. Leave it running.

3. **Terminal 2: start the API with the test switch.** The switch must be on the same line, and `NODE_ENV` must **not** be `production`.
   ```bash
   cd apps/cloud && LOCALAI_GATEWAY_FAKE_UPSTREAM=1 npm run start:dev --workspace=api
   ```
   **What you should see:** a yellow warning `LOCALAI_GATEWAY_FAKE_UPSTREAM is ON ... TEST ONLY`, then `API listening on :3000/api`. Leave it running.
   - **Check it works:** in another terminal, `curl -s http://127.0.0.1:3000/api/gateway/v1/models -H "Authorization: Bearer $(cat ~/.config/opencode/localai-gateway-key)"` shows one model, `fake-load-model`.

4. **Terminal 3: watch the memory.** Find the API's process number and start the sampler (it writes a line every 5 seconds until the API stops):
   ```bash
   apps/cloud/apps/api/test/load/sample-rss.sh "$(pgrep -n -f 'dist/main')" rss.csv
   ```
   **What you should see:** nothing at first; `rss.csv` grows by a row every 5 seconds. If it says `no process with pid`, the API is not running yet.

5. **Terminal 4: run the test.** It takes about 6.5 minutes (1 minute to ramp up, 5 minutes at 200 streams, 30 seconds to ramp down).
   ```bash
   LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)" SCENARIO=fake \
     BASE_URL=http://127.0.0.1:3000/api/gateway/v1 \
     k6 run apps/cloud/apps/api/test/load/gateway-load.k6.js
   ```
   **What you should see:** a progress bar, then a summary like this, ending in seven lines that start with `PASS`:
   ```text
   Gateway load test, scenario fake (http://127.0.0.1:3000, model fake-load-model)
     added latency per event (ms)   p50 1.00   p95 3.00   p99 8.00   max 40.00   (target: p95 <= 5)
     streams started / completed    9810 / 9810   incomplete 0
     errors                         failed-stream rate 0.00%   5xx 0   error events 0
   ```
   A `FAIL` line, or k6 ending with `thresholds ... have been crossed`, means the test did not pass.

6. **Read the memory.** Stop the API (Ctrl + C in terminal 2); the sampler prints `max RSS ... MiB`. It must be **below 600**. The same number is the largest value in the second column of `rss.csv`.

7. **Measure the test's own delay (baseline).** The pretend computer and k6 add a little delay by themselves. Run step 5 again with `BASE_URL=http://127.0.0.1:18999/v1` (straight at the pretend computer, no gateway; the API can be stopped). Write both results down; the gateway's own cost is the difference.

8. **Record it.** Copy `apps/cloud/apps/api/test/load/RESULTS-TEMPLATE.md`, fill in every row, and paste it into the pull request or issue. The spec requires the results to be recorded.

**If it doesn't work**
- **k6 says `LAI_KEY is required`:** the key file is empty or the path is wrong.
- **Every stream fails with status 401 (`LAI-GW-010`):** the key is wrong, revoked or expired. Make a new one.
- **Status 403 (`LAI-GW-012`):** the key is limited to some models; allow `fake-load-model` or all models.
- **Status 404 (`LAI-GW-020`) or 503 (`LAI-GW-021`):** the API was not started with `LOCALAI_GATEWAY_FAKE_UPSTREAM=1`, or the pretend computer is not running (`curl http://127.0.0.1:18999/health` must answer `{"status":"ok"}`).
- **Status 429 (`LAI-GW-030` or `LAI-GW-031`):** the key's limits are too low for 200 streams. Make a key with higher limits.
- **503 (`LAI-GW-023`) for some streams:** more than 200 streams were asked for; the pretend computer takes 512 at most.
- **The API stops at once with `FakeUpstreamRefusedError`:** `NODE_ENV` is `production`, or `LOCALAI_GATEWAY_FAKE_UPSTREAM_URL` is not an `http://` address on this computer (`127.0.0.1`, `localhost` or `::1`). This is on purpose; see [07-troubleshooting.md](07-troubleshooting.md) for the rest.
- **The delay is above 5 ms but the baseline is also high:** the computer is too busy (k6, the pretend computer and the API all share it). Close other programs and run again; note the machine in the results.
- **The delay is above 5 ms and the baseline is low:** that is a real finding. Open a bug issue (area `gateway`) against the streaming code ([#142](https://github.com/marinoscar/local-ai/issues/142)) with the filled results.

---

## Part 2. The real-lab test (owner only, outside working hours)

This sends real questions to the real computers. Do it when nobody is using the chat.

1. **Use the service key** that Open WebUI uses, or make a new service key, and save it in `~/.config/opencode/localai-gateway-key` as in Part 1.
2. **Pick a model** that is loaded on the computers you want to test. Its id is in the **Lab** page, or `curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $(cat ~/.config/opencode/localai-gateway-key)"`.
3. **Run it.** It keeps **8 streams per computer** busy for 10 minutes. Set `NODES` to the number of computers that serve the model.
   ```bash
   LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)" SCENARIO=lab MODEL=<model-id> NODES=3 \
     k6 run apps/cloud/apps/api/test/load/gateway-load.k6.js
   ```
   `BASE_URL` already points at `https://ai.marin.cr/v1`.
4. **While it runs, look at each computer.** On each one, `sudo aictl status` shows how many requests are in progress; it must match that computer's `--parallel` setting for the model. Also open Open WebUI and send a short question: it must still answer.
5. **Pass means:** the summary shows `PASS` for `gateway_5xx` and `stream_error_events` (no 5xx from the gateway), the in-progress count matched on every computer, and Open WebUI stayed usable. Record it in the results file (the "Real lab scenario" part).

**If it doesn't work**
- **Status 429:** the service key's limits are lower than `NODES x 8`; ask a gateway administrator to raise them.
- **Status 503 `LAI-GW-021` or `LAI-GW-023`:** the model is not loaded on enough computers, or they are all busy. Check the **Lab** page.
- **Anything else:** see [07-troubleshooting.md](07-troubleshooting.md).

---

## Part 3. Settings you may change

All are environment variables on the k6 command. The defaults are the test the spec asks for.

| Variable | Default | Meaning |
|---|---|---|
| `SCENARIO` | `fake` | `fake` or `lab` |
| `BASE_URL` | local API (fake), `https://ai.marin.cr/v1` (lab) | where the gateway is; ends in `/v1` |
| `MODEL` | `fake-load-model` (fake) | required for `lab` |
| `VUS`, `RAMP_S`, `HOLD_S`, `RAMPDOWN_S` | 200, 60, 300, 30 | fake: how many streams, and how long each phase lasts |
| `TOKENS`, `INTERVAL_MS` | 300, 20 | fake: must equal the pretend computer's `--tokens` and `--interval-ms` |
| `NODES`, `PER_NODE`, `DURATION`, `MAX_TOKENS`, `PROMPT` | 1, 8, `10m`, 256, a short story | lab |
| `SUMMARY_JSON` | none | also write the numbers to this file |

For a quick check (2 minutes): `VUS=50 RAMP_S=10 HOLD_S=60 RAMPDOWN_S=10`.

## How it is built (for the curious)

- The **test switch** `LOCALAI_GATEWAY_FAKE_UPSTREAM=1` makes the API use one pretend node, one pretend key and a one-model catalog, all in memory; nothing real is changed and nothing is written to the database. It is refused when `NODE_ENV=production`, only accepts an address on the same computer, and appears in no deployment file. Details: [gateway.md §9, "As built (GW-12)"](../specs/gateway.md#load-testing).
- The files: `apps/cloud/apps/api/test/load/` (`gateway-load.k6.js`, `fake-upstream.mjs`, `sample-rss.sh`, `RESULTS-TEMPLATE.md`).
