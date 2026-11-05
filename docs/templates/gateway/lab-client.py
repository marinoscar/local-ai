import os
from openai import OpenAI
client = OpenAI(base_url="https://ai.marin.cr/v1", api_key=open(os.path.expanduser("~/.config/opencode/localai-gateway-key")).read().strip())
for chunk in client.chat.completions.create(model="qwen3.5-9b-q4km", stream=True, messages=[{"role": "user", "content": "Hola"}]):
    print(chunk.choices[0].delta.content or "", end="", flush=True) if chunk.choices else None
