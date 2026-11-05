# Gateway client templates

Copy-ready settings for the lab gateway, `https://ai.marin.cr/v1` ([gateway.md Appendix A](../../specs/gateway.md#appendix-a-client-wiring)). They hold **no key**: every key is a file reference, a placeholder, or typed by the person at run time.

| File | What it is | Used by |
|---|---|---|
| [`opencode.localai-lab.json`](opencode.localai-lab.json) | The whole `~/.config/opencode/opencode.json` for a computer that uses only the lab (Appendix A.2) | [02 step 14](../../runbooks/02-chat-and-api-keys.md) |
| [`opencode.node-and-lab.json`](opencode.node-and-lab.json) | The same, plus the `local-llama` provider of the node itself, which keeps working with no internet. Use this on a node | [02 step 14](../../runbooks/02-chat-and-api-keys.md) |
| [`openwebui.env.example`](openwebui.env.example) | The two lines of `apps/openwebui/.env` in the private `marin-server-infra` repository | [02 step 20](../../runbooks/02-chat-and-api-keys.md) |
| [`lab-client.py`](lab-client.py) | A streaming Python client (Appendix A.3) | [02 step 28](../../runbooks/02-chat-and-api-keys.md) |

The key file is always `~/.config/opencode/localai-gateway-key`, mode 600, and one stored key serves OpenCode, curl and Python.

These files are machine-checked by `scripts/tests/gateway-docs.test.mjs` (they parse, they equal the spec's blocks, they contain no key-shaped text). The helper `scripts/gateway-client.sh` writes the OpenCode block for you.
