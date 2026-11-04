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

14. **OpenCode anywhere, through the lab (needs internet).** Save the `lai_` key:

   ```
   install -m 600 /dev/stdin ~/.config/opencode/localai-gateway-key
   ```

   (paste, **Enter**, **Ctrl + D**). Add a provider named `localai-lab` to `~/.config/opencode/opencode.json`, as in [gateway.md Appendix A.2](../specs/gateway.md#a2-opencode-configopencodeopencodejson): `"baseURL": "https://ai.marin.cr/v1"`, `"apiKey": "{file:~/.config/opencode/localai-gateway-key}"`.
   - **Keep the local provider too on nodes:** it works offline; the lab one doesn't.

---

## Part 5. Connect Open WebUI

15. **The lab's Open WebUI (`https://chat.marin.cr`)** should use **one** connection to the gateway instead of one per node. The lab owner does this once, following [gateway.md Appendix A.1](../specs/gateway.md#a1-open-webui-chatmarincr-one-connection-instead-of-one-per-node): create a **service** key named `open-webui`, then in Open WebUI **Admin Panel → Settings → Connections → OpenAI API → +**, URL `https://ai.marin.cr/v1`, the `lai_` key, **Save**, then the refresh icon.
   - **What you should see:** the lab models appear in the model list. A test chat shows up in ai.marin.cr → **Usage** on the `open-webui` key.
   > 🛠 Verify when implemented (Epic E7). Until then Open WebUI keeps one connection per node (`https://ai-n1.marin.cr/v1`, `https://ai-n2.marin.cr`, `https://ai-n3.marin.cr/v1`), each with that node's key.

16. **An Open WebUI running on the node itself:** add a connection with URL `http://127.0.0.1:8080/v1` and the node key (desktop **Show connection info → Open WebUI** tab).

---

## Part 6. curl and Python

17. **On the node (offline is fine).** Put the key in a variable for this terminal only, then ask a question. Replace the model with one that is loaded (`aictl status` shows it after **Brain**):

   ```
   export LLAMA_API_KEY=$(aictl key show --json | jq -r .key)
   curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
   curl -s http://127.0.0.1:8080/v1/chat/completions -H "Authorization: Bearer $LLAMA_API_KEY" \
     -H 'Content-Type: application/json' \
     -d '{"model":"qwen3.5-9b-q4km","messages":[{"role":"user","content":"Hello"}]}'
   ```

   - **What you should see:** a JSON list of models, then a JSON answer with `"content": "…"`.
   - **If it doesn't work:** `401`: wrong or old key. Connection refused: the AI service is down, run `aictl doctor`. An error about the model: it isn't loaded ([03](03-swap-a-model.md)).

18. **From another computer, through the node's public address.** Type the key without it appearing on screen or in history:

   ```
   read -rsp "Node API key: " LLAMA_API_KEY; echo
   curl -s https://ai-n3.marin.cr/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
   unset LLAMA_API_KEY
   ```

   - **What you should see:** the model list. Without the key the same address answers `401`; that's correct.
   - **If it doesn't work:** `502` or no answer: the node is offline or not published, see [07 → Cloud shows node offline](07-troubleshooting.md#cloud-shows-node-offline).

19. **Through the lab gateway, with a `lai_` key:**

   ```
   export LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)"
   curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $LAI_KEY"
   curl -N https://ai.marin.cr/v1/chat/completions -H "Authorization: Bearer $LAI_KEY" \
     -H "Content-Type: application/json" \
     -d '{"model":"qwen3.5-9b-q4km","stream":true,"messages":[{"role":"user","content":"Hola"}]}'
   ```

   - **What you should see:** the lab's models, then an answer streaming in. The response header `x-localai-node` says which node answered.
   - **If it doesn't work:** the error has a `LAI-GW-…` code and a fix. `LAI-GW-010`: key missing, wrong, revoked or expired. `LAI-GW-021`: no node can run that model right now. `LAI-GW-022`: the model is loading, wait a minute. Full list: [gateway.md §8](../specs/gateway.md#8-doctor-codes-lai-gw).

20. **Python** (needs the `openai` package: `pip install openai`, with internet, once):

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

   Old model names from before the migration (for example `qwen3.5-9b` on ai-n3) still work as aliases.

---

## Part 7. Rotating keys, and what breaks

Rotate a key when it may have leaked (pasted somewhere, a laptop lost, a person leaving).

21. **Rotate the node key.** Desktop: **Show connection info → Rotate key** (asks the PIN). Cloud: node page → **Keys** tab → **Rotate key…** (lab admin). Terminal:

   ```
   sudo aictl key rotate
   ```

   - **What you should see:** a confirmation question, then `✓ New key created (fingerprint …). The model was started again.` and a list of programs to update. The AI stops for a few seconds while it restarts.
   - **What breaks until you update it:** OpenCode on the node (`~/.config/opencode/llama-api-key`, repeat step 13), any script with the old key, and any client that calls `https://ai-nN.marin.cr` directly with the old key (before E7 that includes the Open WebUI connection for that node: update it in **Admin Panel → Settings → Connections**).
   - **What keeps working by itself:** the cloud and the gateway (`https://ai.marin.cr/v1`) get the new key automatically. Clients using a `lai_` key need nothing.
   - **If it doesn't work:** exit with `LAI-LLM-001`: the key **was** rotated but the AI didn't restart; run `aictl restart llama`. Gateway errors `LAI-GW-043` afterwards: run `sudo aictl cloud resend-key` on the node.

22. **Replace a lab key (`lai_…`).** At `https://ai.marin.cr` → **API keys**, press **Revoke** on the old key and confirm with **Revoke** in the window that asks, then **Create key** (Part 3) and put the new key into the program. The old key stops working on the next request.

23. **The agent token** (`lat_…`, used by the desktop app and `aictl` over the network fallback; not an AI key):

   ```
   sudo aictl token rotate
   ```

   The desktop app picks up the new one by itself. Scripts that copied the old token must read `/etc/localai/agent-token` again.
