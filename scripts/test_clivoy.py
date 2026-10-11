"""Send one message to Clivoy with the official OpenAI Python SDK."""

import os

from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:9610/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
)

response = client.chat.completions.create(
    model="ollama/gpt-oss:20b",  # Change this value; run `clivoy models` for names.
    messages=[{"role": "user", "content": "안녕하세요! 한 문장으로 답해주세요."}],
)

print(response.choices[0].message.content)
