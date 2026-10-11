"""Use the official OpenAI SDK against Clivoy (text-only, CLI-safe example)."""

import os

from openai import OpenAI


with OpenAI(
    base_url=os.getenv("CLIVOY_BASE_URL", "http://127.0.0.1:9610").rstrip("/") + "/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
) as client:
    model = os.getenv("CLIVOY_MODEL", "codex")
    response = client.responses.create(
        model=model,
        input="Explain what an API gateway does in one sentence.",
        store=False,
    )
    print(response.output_text)

    with client.chat.completions.create(
        model=model,
        messages=[{"role": "user", "content": "Say hello in Korean."}],
        stream=True,
    ) as stream:
        for chunk in stream:
            if chunk.choices:
                print(chunk.choices[0].delta.content or "", end="", flush=True)
    print()
