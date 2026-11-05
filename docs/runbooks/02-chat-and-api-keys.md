# 02. Chat and API keys

Use this when you want to chat with a node's AI, or connect a program (OpenCode, Open WebUI, curl, a Python script) to one node or to the whole lab.

> **Before you start**
> - You need: a node that is running (see `aictl status`), and a user that is in the `localai-operators` group (the installer adds the main desktop user).
> - For the lab-wide URL `https://ai.marin.cr/v1`: internet, and an account on `https://ai.marin.cr` with permission to create API keys.
> - **Time:** 5 minutes to chat; 10 to 15 minutes to connect a program.

> 🛠 Verify when implemented (Epic E1 agent and `aictl`, E8 desktop app, E6 cloud, E7 gateway).

**Words used on this page**
- **WebUI:** the chat page built into the AI program (llama.cpp). It's the same chat page everywhere.
- **API:** the way programs talk to the AI. Ours is "OpenAI-compatible", so any tool that works with OpenAI can be pointed at it.
- **Base URL:** the address a program sends its questions to, ending in `/v1`.
- **API key:** a secret password a program sends with every question. Without it the node answers `401` (not allowed).
- **Service key:** a lab key (`lai_…`) made for a program instead of a person, such as Open WebUI. Only a gateway administrator can create one. It does not have to expire.

## Which address and which key?

There are two kinds of keys. Don't mix them up.

| Base URL | Works when | Key to use |
|---|---|---|
| `http://127.0.0.1:8080/v1` | On the node itself. **Works with no internet** | The **node key** (64 letters and digits, from this node) |
| `https://ai-nN.marin.cr/v1` (for example `https://ai-n3.marin.cr/v1`) | From anywhere, node in **online** mode | The **node key** of that node |
| The tailnet URL shown by `aictl key show` | From computers on the lab's Tailscale network, node online | The **node key** of that node |
| `https://ai.marin.cr/v1` (the lab gateway) | From anywhere, with internet. It picks a node that has the model | A **lab key** starting `lai_`, from the cloud |

A node key never works on `https://ai.marin.cr/v1`, and a `lai_` key never works on a node directly.

---

## Part 1. Chat in the browser

1. **From the Local AI app:** press the **Chat** button at the top of the window.
   - **What you should see:** a new window with the chat page. Type in the box at the bottom and press **Enter**.
   - **If it doesn't work:** the app shows an error page with a code. See [07 → The app won't open](07-troubleshooting.md#the-app-wont-open).

2. **From any browser on the node, without the app:** open a terminal (**Ctrl + Alt + T**) and get a one-time login code:

   ```
   aictl ui-code
   ```

   Then open `http://127.0.0.1:7070` in the browser, type the 6-digit code, and press **Chat**.
   - **What you should see:** `Login code  482 913   (valid for 5 minutes, one use)`, then the dashboard after you type it.
   - **If it doesn't work:** `LAI-SEC-008` means the code is wrong, used or expired: run `aictl ui-code` again. `LAI-CLI-002`: your user isn't an operator, see [07 → The app won't open](07-troubleshooting.md#the-app-wont-open).

3. **Pick the model** in the drop-down at the top of the chat page. Only models that are **loaded** answer. To load another one, see [03-swap-a-model.md](03-swap-a-model.md).
   - **What you should see:** the names are catalog ids such as `qwen3.5-9b-q4km`.
   - **If it doesn't work:** an error after choosing a model means it isn't loaded. This is on purpose: models are loaded through the agent so they can't push each other out of memory ([03](03-swap-a-model.md)).

4. **Chat for the whole family, in the lab:** `https://chat.marin.cr` (Open WebUI). It needs its own account; ask the lab owner. It only works with internet.

In **Survival mode**, button **1 Chat** in the Portal opens the same chat page with no login ([01](01-survival-mode.md)).

---

## Part 2. Get the node key

Treat the key like a password. Don't paste it in chats, emails or screenshots.

5. **Desktop app:** press **Copy API key** at the top of the window.
   - **What you should see:** "API key copied. It will be cleared from the clipboard in 60 seconds." Paste it where you need it right away.
   - **If it doesn't work:** use step 7.

6. **Desktop app, with URLs and ready-made settings:** menu **⋮** → **Show connection info**. It shows the local URL, this node's public URL, the lab gateway URL, the key hidden (press **Reveal** or **Copy**) and tabs with settings for **OpenCode**, **Open WebUI** and **curl**.

7. **Terminal:** copy the key to the clipboard without showing it:

   ```
   aictl key copy
   ```

   - **What you should see:** `✓ The API key is on the clipboard.`
   - **If it doesn't work:** `LAI-CLI-011` (no clipboard tool, common over SSH): use step 8.

8. **Terminal, show everything** (key, fingerprint, URLs, models):

   ```
   aictl key show
   ```

   - **What you should see:** a warning line, then `Key`, `Fingerprint`, `Local URL`, `Tailnet URL`, `Public URL` and `Models`. Each use is written to the node's audit log.
   - **If it doesn't work:** `LAI-CLI-002`: your user isn't in `localai-operators` ([07](07-troubleshooting.md#the-app-wont-open)). `LAI-SEC-010`: the key file is damaged; run `sudo aictl key rotate` (Part 7 explains what that breaks).

9. **Ready-made snippets** for a program:

   ```
   aictl key snippets --for opencode
   aictl key snippets --for curl --url public
   aictl key snippets --for python
   ```

   The key appears as `$LLAMA_API_KEY` in the snippet unless you add `--with-key`.

---

## Part 3. Get a lab key (`lai_…`) for `https://ai.marin.cr/v1`

> 🛠 Verify when implemented (Epics E6, E7). The button and field names below are the ones in the page (GW-02); the steps have not been followed on a live deployment yet.

10. Open `https://ai.marin.cr` and sign in with Google.
11. Go to **API keys** (in the side bar, or the bottom bar on a phone) → **Create key**. Type a **Name** you will recognise (for example `OpenCode laptop`), leave **Models** empty to allow every model (or type a model name and press **Enter** to allow only that one), and set **Expires in (days)**. Press **Create**.
   - **What you should see:** a box with your key, starting `lai_`, shown **once**, with a **Copy** button and three tabs of ready snippets (**curl**, **OpenCode**, **Open WebUI**). "Store it now; it can't be shown again." Press **Copy**, store the key (next step), and only then press **I have stored it**. The window will not close any other way, because the key can never be shown again.
   - **If it doesn't work:** no **API keys** entry: your account doesn't have the `gateway:use` permission. Ask the lab owner. The page says "Unknown model": you typed a model name the lab doesn't have; leave **Models** empty or use a name from the suggestions.
12. Store it in a private file on your computer. Type this, paste the key, press **Enter**, then **Ctrl + D**:

   ```
   mkdir -p ~/.config/opencode
   install -m 600 /dev/stdin ~/.config/opencode/localai-gateway-key
   ```

   - **What you should see:** nothing; the file now holds the key, readable only by you.

---

## Part 4. Connect OpenCode

13. **OpenCode on the node (works offline).** Save the node key in the file OpenCode reads:

   ```
   mkdir -p ~/.config/opencode
   (umask 077; aictl key show --json | jq -r .key > ~/.config/opencode/llama-api-key)
   ```

   Then add the provider block from `aictl key snippets --for opencode` (or the desktop's **OpenCode** tab) to `~/.config/opencode/opencode.json`. It points at `http://127.0.0.1:8080/v1` with `"apiKey": "{file:~/.config/opencode/llama-api-key}"`.
   - **What you should see:** in a project folder, `opencode run "say hi"` answers.
   - **If it doesn't work:** `401` means the key file is old (the key was rotated): repeat this step. See [07 → API key doesn't work (401)](07-troubleshooting.md#api-key-doesnt-work-401).

14. **OpenCode anywhere, through the lab (needs internet).** This uses the `lai_` key of Part 3, stored in `~/.config/opencode/localai-gateway-key` (step 12). If you have not stored it yet, do that first:

   ```
   install -m 600 /dev/stdin ~/.config/opencode/localai-gateway-key
   ```

   (paste the key, press **Enter**, then **Ctrl + D**). Now tell OpenCode about the lab. Choose one:

   - **A computer with no OpenCode settings yet:** save this as `~/.config/opencode/opencode.json` ([gateway.md Appendix A.2](../specs/gateway.md#a2-opencode-configopencodeopencodejson); the same file is [`opencode.localai-lab.json`](../templates/gateway/opencode.localai-lab.json)):

     ```json
     {
       "$schema": "https://opencode.ai/config.json",
       "model": "localai-lab/qwen3-coder-next-q4km",
       "provider": {
         "localai-lab": {
           "npm": "@ai-sdk/openai-compatible",
           "name": "Local AI lab (ai.marin.cr)",
           "options": {
             "baseURL": "https://ai.marin.cr/v1",
             "apiKey": "{file:~/.config/opencode/localai-gateway-key}",
             "timeout": 600000
           },
           "models": {
             "qwen3-coder-next-q4km": { "name": "Qwen3 Coder Next (lab)", "limit": { "context": 131072, "output": 8192 } },
             "qwen3.5-9b-q4km": { "name": "Qwen 3.5 9B (lab)", "limit": { "context": 32768, "output": 8192 } }
           }
         }
       }
     }
     ```

   - **A computer that already has OpenCode settings:** from a copy of this repository, run `scripts/gateway-client.sh opencode`. It saves a backup of your file, adds only the `localai-lab` provider and keeps everything else (add `--default` to make the lab model your default). If your file has comments in it, the script stops without changing anything; then paste the `provider` entry above into yours by hand.
   - **On a node:** use [`opencode.node-and-lab.json`](../templates/gateway/opencode.node-and-lab.json), or the helper above. **Keep the local provider (`local-llama`, `http://127.0.0.1:8080/v1`) on nodes:** it works offline; the lab provider does not.

   - **What you should see:** `scripts/gateway-client.sh check` prints `ok   no key -> 401 LAI-GW-010`, then `ok   key -> 200, models:` and one line per model with its state (`loaded` or `loadable`). Then `opencode run --model localai-lab/qwen3.5-9b-q4km "say hi"` answers, and ai.marin.cr → **Usage** shows the request on your key.
   - **If it doesn't work:** `401` or `LAI-GW-010`: the key file is wrong, revoked or expired; create a new key (Part 3) and store it again. `LAI-GW-012`: your key may not use that model. `LAI-GW-020`: no such model id; run the `check` and use an id from its list. `LAI-GW-021`: no node can run it right now, try another model. No internet: the lab provider cannot work; use `local-llama`. More: [07 → API key doesn't work (401)](07-troubleshooting.md#api-key-doesnt-work-401).

---

## Part 5. Connect Open WebUI

This is a **one-time job for the lab owner** (a gateway administrator who can create a **service** key and who is the admin of `https://chat.marin.cr`). Today Open WebUI has one connection per node. After this part it has **one** connection, `https://ai.marin.cr/v1`, and the gateway picks the node. The owner's checklist, with the rollback, is in [10 → Part 5](10-lab-operations.md#part-5-gateway-keys-and-usage). This part is the procedure ([gateway.md Appendix A.1](../specs/gateway.md#a1-open-webui-chatmarincr-one-connection-instead-of-one-per-node)).

> 🛠 Verify when implemented (Epic E7). The steps follow the page names of the cloud app and of Open WebUI; they have not been followed on the live lab yet. Menu names in Open WebUI change between releases: if one differs, look for the same word.

> **Before you start**
> - The cloud is up and the nodes show as online with a loaded model (`https://ai.marin.cr` → **Lab**).
> - You can sign in to `https://chat.marin.cr` as admin.
> - **Write down what Open WebUI shows today** (Admin Panel → Settings → Connections → OpenAI API: the URLs). If you must go back, you will need it.
> - Time: 15 minutes. Do it when nobody is in the middle of a long chat; connections change on Save.

15. **Create the service key.** At `https://ai.marin.cr` go to **API keys** → **Create key**. Name: `open-webui`. Key type: **Service key (a program such as Open WebUI)**. Models: leave empty (every model). Expires in (days): leave empty (never). Press **Create**, press **Copy**, and keep the key where the next step can use it (a password manager). Only then press **I have stored it**.
   - **What you should see:** a box with a key starting `lai_`, shown once.
   - **If it doesn't work:** no **Service key** choice: your account is not a gateway administrator; ask the lab owner. A service key keeps working only while its owner's account is active and allowed to use the gateway (`LAI-GW-013` otherwise), so create it with the account that will stay.

16. **Open the connections page.** Sign in to `https://chat.marin.cr` as admin → **Admin Panel** → **Settings** → **Connections** → **OpenAI API**.
   - **What you should see:** the list of today's connections (`https://ai-n1.marin.cr/v1`, `https://ai-n2.marin.cr`, `https://ai-n3.marin.cr/v1`, maybe `https://api.openai.com/v1`).

17. **Add the gateway.** Click **+**. URL `https://ai.marin.cr/v1`, auth **Bearer**, key = the `lai_` key, prefix ID empty. **Save**. Click the refresh icon.
   - **What you should see:** the lab models appear in the list (ids such as `qwen3.5-9b-q4km`).
   - **If it doesn't work:** an error on Save or no models: the key was pasted with a space or cut short, or it is not a `lai_` key (a node key does not work here). Test it outside Open WebUI: `scripts/gateway-client.sh check`. `LAI-GW-010`: wrong or revoked key, create it again (step 15). `LAI-GW-051`: the gateway is off or the site is in maintenance. See [07 → Open WebUI shows no lab models](07-troubleshooting.md#open-webui-shows-no-lab-models).

18. **Remove the per-node connections.** Delete (trash icon) the connections `https://ai-n1.marin.cr/v1`, `https://ai-n2.marin.cr` and `https://ai-n3.marin.cr/v1`. **Keep `https://api.openai.com/v1` if it is used.** **Save**.
   - **What you should see:** only the gateway (and OpenAI, if you keep it) in the list.
   - **If it doesn't work:** a trash icon does nothing or the list comes back after a reload: do not click more; go to step 22 only after reading its warning.

19. **Try it.** Open a **new chat**, pick `qwen3.5-9b-q4km` (or any listed id), send `hola`.
   - **What you should see:** an answer, and at `https://ai.marin.cr` → **API keys** → **Usage** (the Key filter) a request on the `open-webui` key a few seconds later.
   - **If it doesn't work:** the model in the picker says not found: press the refresh icon in Connections and pick again. `LAI-GW-021`: no node has that model loaded; pick another or load it ([03](03-swap-a-model.md)). `LAI-GW-022`: it is loading, wait a minute.

20. **Make a fresh install match** (the lab owner, in the private `marin-server-infra` repository, file `apps/openwebui/.env`; this repository only documents it). Set the two lines of [`openwebui.env.example`](../templates/gateway/openwebui.env.example): `OPENAI_API_BASE_URL=https://ai.marin.cr/v1` and `OPENAI_API_KEY=` the service key. Open WebUI reads these only on a fresh install, but the check in step 21 reads the second one. Then recreate the container (`docker compose up -d` in `/opt/infra/apps/openwebui`).
   - **What you should see:** `docker compose ps` shows `open-webui` running again.

21. **Check from the VPS** (as root). Copy [`scripts/openwebui-cutover.sh`](../../scripts/openwebui-cutover.sh) to the VPS (it is one file; read it first, it only reads settings), then:

   ```
   ./openwebui-cutover.sh check
   ./openwebui-cutover.sh verify
   ```

   - **What you should see:** `ok   the gateway (https://ai.marin.cr/v1) is configured once`, `ok   no per-node connection is left`, `ok   urls, keys and configs are index-aligned (…)`, then `200` and `ok: the gateway accepts the key in the container's environment`. The script never prints a key.
   - **If it doesn't work:** `FAIL per-node connections still present`: go back to step 18. `401` in `verify`: `OPENAI_API_KEY` in the container is not the service key; redo step 20. If the script says Open WebUI's settings table is not the one the spec describes, that is only the script: the Admin Panel result of steps 17 to 19 is what counts.

22. **Only if the Admin Panel is broken: change the database. This is a last resort.** **Do not use this path while the Admin Panel works.** The three settings are **index-aligned lists**: edit all three together or none. Open WebUI's stored format changes between releases, so first **back up**:

   ```bash
   docker exec infra-postgres psql -U admin -d open_webui -Atc \
     "select key, value from config where key in ('openai.api_base_urls','openai.api_keys','openai.api_configs');" \
     > /root/openwebui-connections.backup.txt
   chmod 600 /root/openwebui-connections.backup.txt
   ```

   (`./openwebui-cutover.sh backup` does the same and also refuses to overwrite an earlier backup.) **Read the backup file** and decide the new lists: `["https://api.openai.com/v1","https://ai.marin.cr/v1"]`, or only the gateway. Keep the OpenAI key at its index and renumber `openai.api_configs` (keys `"0"`, `"1"`, each `{"enable": true, "prefix_id": "", "model_ids": []}`). Type the gateway key, never store it in shell history:

   ```bash
   read -rs GW_KEY   # paste the lai_ key (not echoed)
   OPENAI_KEY_JSON="$(docker exec infra-postgres psql -U admin -d open_webui -Atc \
     "select value::jsonb->1 from config where key='openai.api_keys';")"   # 1 = index of the api.openai.com entry today; check the backup
   docker exec -i infra-postgres psql -U admin -d open_webui -v ON_ERROR_STOP=1 <<SQL
   BEGIN;
   UPDATE config SET value = '["https://api.openai.com/v1","https://ai.marin.cr/v1"]'::json WHERE key = 'openai.api_base_urls';
   UPDATE config SET value = json_build_array('${OPENAI_KEY_JSON}'::json, '${GW_KEY}') WHERE key = 'openai.api_keys';
   UPDATE config SET value = '{"0":{"enable":true,"prefix_id":"","model_ids":[]},"1":{"enable":true,"prefix_id":"","model_ids":[]}}'::json
    WHERE key = 'openai.api_configs';
   COMMIT;
   SQL
   unset GW_KEY OPENAI_KEY_JSON
   ```

   Then restart Open WebUI (`docker compose restart open-webui` in `/opt/infra/apps/openwebui`), wait 20 to 40 seconds, and check without printing the key (this reads `OPENAI_API_KEY` in the container, so it proves the gateway key only after step 20):

   ```bash
   docker exec open-webui sh -c 'curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $OPENAI_API_KEY" https://ai.marin.cr/v1/models'
   ```

   - **What you should see:** `200`, then the lab models in a new chat.
   - **If it doesn't work:** `ERROR` from `psql` (the table is not `config(key, value)`): nothing was changed because of `BEGIN`/`ON_ERROR_STOP`; use the Admin Panel. `502` from `chat.marin.cr`: it is still starting, wait. `401`: see step 21. To go back, step 23.

23. **Rollback (go back to one connection per node).** Pick the way that fits:
   - **Admin Panel:** Connections → **+** for each node: `https://ai-n1.marin.cr/v1`, `https://ai-n2.marin.cr/v1`, `https://ai-n3.marin.cr/v1`, auth **Bearer**, key = **that node's key** (`aictl key show` on the node, or the node's page in the cloud), then delete the gateway connection if you want. Compare with what you wrote down before step 16.
   - **From the backup** (only if you made one in step 22): `./openwebui-cutover.sh restore --yes`, then `docker compose restart open-webui` and wait 20 to 40 seconds.
   - **What you should see:** the old model list in a new chat. The service key can stay; revoke it if you will not retry ([10 → Part 5](10-lab-operations.md#part-5-gateway-keys-and-usage)).

24. **An Open WebUI running on the node itself:** add a connection with URL `http://127.0.0.1:8080/v1` and the node key (desktop **Show connection info → Open WebUI** tab). It works offline.

---

## Part 6. curl and Python

25. **On the node (offline is fine).** Put the key in a variable for this terminal only, then ask a question. Replace the model with one that is loaded (`aictl status` shows it after **Brain**):

   ```
   export LLAMA_API_KEY=$(aictl key show --json | jq -r .key)
   curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
   curl -s http://127.0.0.1:8080/v1/chat/completions -H "Authorization: Bearer $LLAMA_API_KEY" \
     -H 'Content-Type: application/json' \
     -d '{"model":"qwen3.5-9b-q4km","messages":[{"role":"user","content":"Hello"}]}'
   ```

   - **What you should see:** a JSON list of models, then a JSON answer with `"content": "…"`.
   - **If it doesn't work:** `401`: wrong or old key. Connection refused: the AI service is down, run `aictl doctor`. An error about the model: it isn't loaded ([03](03-swap-a-model.md)).

26. **From another computer, through the node's public address.** Type the key without it appearing on screen or in history:

   ```
   read -rsp "Node API key: " LLAMA_API_KEY; echo
   curl -s https://ai-n3.marin.cr/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
   unset LLAMA_API_KEY
   ```

   - **What you should see:** the model list. Without the key the same address answers `401`; that's correct.
   - **If it doesn't work:** `502` or no answer: the node is offline or not published, see [07 → Cloud shows node offline](07-troubleshooting.md#cloud-shows-node-offline).

27. **Through the lab gateway, with a `lai_` key** ([gateway.md Appendix A.3](../specs/gateway.md#a3-curl-and-python)). It uses the same key file as OpenCode, so one stored key serves every program (step 12). `jq` is needed for the first and third command:

   ```bash
   export LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)"
   curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[] | {id, state: .["x-localai"].state}'
   curl -N https://ai.marin.cr/v1/chat/completions -H "Authorization: Bearer $LAI_KEY" -H "Content-Type: application/json" \
     -d '{"model":"qwen3.5-9b-q4km","stream":true,"messages":[{"role":"user","content":"Name three edible plants in Costa Rica."}]}'
   curl -s https://ai.marin.cr/v1/embeddings -H "Authorization: Bearer $LAI_KEY" -H "Content-Type: application/json" \
     -d '{"model":"<embedding-id>","input":"agua potable"}' | jq '.data[0].embedding | length'
   ```

   Replace `<embedding-id>` with an embedding model from the first command's list.
   - **What you should see:** first, one line per model, such as `{"id":"qwen3.5-9b-q4km","state":"loaded"}` (`loaded` answers now; `loadable` may take a minute to load). Second, the answer **streaming in word by word**; press **Ctrl + C** to stop it, which also stops the work on the node. Third, a number (the size of the embedding). The response header `x-localai-node` (add `-i` to the curl) says which node answered.
   - **If it doesn't work:** the error has a `LAI-GW-…` code and a fix. `LAI-GW-010`: key missing, wrong, revoked or expired; with **no** key at all the answer is `401` with this code, which is correct. `LAI-GW-021`: no node can run that model right now. `LAI-GW-022`: the model is loading, wait a minute. Full list: [gateway.md §8](../specs/gateway.md#8-doctor-codes-lai-gw). Or run `scripts/gateway-client.sh check`.

28. **Python** (needs the `openai` package: `pip install openai`, with internet, once):

   ```python
   import os
   from openai import OpenAI

   # On the node, offline:
   client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key=os.environ["LLAMA_API_KEY"])
   # Through the lab instead:
   # client = OpenAI(base_url="https://ai.marin.cr/v1",
   #                 api_key=open(os.path.expanduser("~/.config/opencode/localai-gateway-key")).read().strip())

   r = client.chat.completions.create(model="qwen3.5-9b-q4km",
                                      messages=[{"role": "user", "content": "Hola"}])
   print(r.choices[0].message.content)
   ```

   Through the lab, **streaming** ([`lab-client.py`](../templates/gateway/lab-client.py), Appendix A.3):

   ```python
   import os
   from openai import OpenAI
   client = OpenAI(base_url="https://ai.marin.cr/v1", api_key=open(os.path.expanduser("~/.config/opencode/localai-gateway-key")).read().strip())
   for chunk in client.chat.completions.create(model="qwen3.5-9b-q4km", stream=True, messages=[{"role": "user", "content": "Hola"}]):
       print(chunk.choices[0].delta.content or "", end="", flush=True) if chunk.choices else None
   ```

   - **What you should see:** the answer, printed as it arrives.
   - **If it doesn't work:** `openai.AuthenticationError` is `LAI-GW-010` (the key file); `NotFoundError` is `LAI-GW-020` (the model id). Old model names from before the migration (for example `qwen3.5-9b` on ai-n3) still work as aliases.

---

## Part 7. Rotating keys, and what breaks

Rotate a key when it may have leaked (pasted somewhere, a laptop lost, a person leaving).

29. **Rotate the node key.** Desktop: **Show connection info → Rotate key** (asks the PIN). Cloud: node page → **Keys** tab → **Rotate key…** (lab admin). Terminal:

   ```
   sudo aictl key rotate
   ```

   - **What you should see:** a confirmation question, then `✓ New key created (fingerprint …). The model was started again.` and a list of programs to update. The AI stops for a few seconds while it restarts.
   - **What breaks until you update it:** OpenCode on the node (`~/.config/opencode/llama-api-key`, repeat step 13), any script with the old key, and any client that calls `https://ai-nN.marin.cr` directly with the old key (until the Open WebUI cutover of Part 5 is done, that includes the Open WebUI connection for that node: update it in **Admin Panel → Settings → Connections**).
   - **What keeps working by itself:** the cloud and the gateway (`https://ai.marin.cr/v1`) get the new key automatically. Clients using a `lai_` key need nothing.
   - **If it doesn't work:** exit with `LAI-LLM-001`: the key **was** rotated but the AI didn't restart; run `aictl restart llama`. Gateway errors `LAI-GW-043` afterwards: run `sudo aictl cloud resend-key` on the node.

30. **Replace a lab key (`lai_…`).** At `https://ai.marin.cr` → **API keys**, press **Revoke** on the old key and confirm with **Revoke** in the window that asks, then **Create key** (Part 3) and put the new key into the program (a service key such as `open-webui`: paste the new one into its Open WebUI connection, step 17). The old key stops working on the next request.

31. **The agent token** (`lat_…`, used by the desktop app and `aictl` over the network fallback; not an AI key):

   ```
   sudo aictl token rotate
   ```

   The desktop app picks up the new one by itself. Scripts that copied the old token must read `/etc/localai/agent-token` again.
