"""Use the official Anthropic SDK against Clivoy."""

import os

from anthropic import Anthropic


with Anthropic(
    # The Anthropic SDK appends /v1/messages itself: no /v1 suffix here.
    base_url=os.getenv("CLIVOY_BASE_URL", "http://127.0.0.1:9610").rstrip("/"),
    api_key=os.environ["CLIVOY_API_KEY"],
) as client:
    with client.messages.stream(
        model=os.getenv("CLIVOY_MODEL", "claude"),
        max_tokens=512,
        messages=[{"role": "user", "content": "Explain what an API gateway does in one sentence."}],
    ) as stream:
        for text in stream.text_stream:
            print(text, end="", flush=True)
        message = stream.get_final_message()
    print(f"\nStop reason: {message.stop_reason}")
