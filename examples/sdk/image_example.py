"""Analyze a local image or generate an image through a configured Clivoy model.

Examples:
  python examples/sdk/image_example.py analyze photo.png --model codex
  python examples/sdk/image_example.py analyze photo.png --model claude --api anthropic
  python examples/sdk/image_example.py generate "A blue bird" --model image-model

Set CLIVOY_API_KEY to the gateway token. These examples invoke your real backend.
"""

import argparse
import base64
import mimetypes
import os
from pathlib import Path

from anthropic import Anthropic
from openai import OpenAI


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    analyze = commands.add_parser("analyze", help="Send a local image and receive text")
    analyze.add_argument("image", type=Path)
    analyze.add_argument("--model", default="codex", help="Clivoy alias with chat and vision capabilities")
    analyze.add_argument("--api", choices=["responses", "chat", "anthropic"], default="responses")
    analyze.add_argument("--prompt", default="Describe this image briefly.")
    generate = commands.add_parser("generate", help="Use an HTTP image-generation backend")
    generate.add_argument("prompt")
    generate.add_argument("--model", required=True, help="Clivoy alias with image_generation capability")
    generate.add_argument("--output", type=Path, help="Save base64 output here; existing files are not overwritten")
    args = parser.parse_args()
    base_url = os.getenv("CLIVOY_BASE_URL", "http://127.0.0.1:9610").rstrip("/")
    key = os.environ["CLIVOY_API_KEY"]

    if args.command == "generate":
        with OpenAI(base_url=base_url + "/v1", api_key=key) as client:
            result = client.images.generate(model=args.model, prompt=args.prompt, n=1)
        if not result.data:
            raise RuntimeError("The backend returned no images")
        item = result.data[0]
        if item.b64_json:
            output = args.output or Path("generated." + (result.output_format or "png"))
            with output.open("xb") as file:
                file.write(base64.b64decode(item.b64_json, validate=True))
            print(output.resolve())
        elif item.url:
            print(item.url)
        else:
            raise RuntimeError("The backend returned neither image bytes nor a URL")
        return

    media_type, _ = mimetypes.guess_type(args.image.name)
    if media_type not in {"image/png", "image/jpeg", "image/webp", "image/gif"}:
        parser.error("Choose a PNG, JPEG, WebP, or GIF image")
    if args.image.stat().st_size > 5 * 1024 * 1024:
        parser.error("Input images must be at most 5 MiB each")
    encoded = base64.b64encode(args.image.read_bytes()).decode("ascii")
    data_uri = f"data:{media_type};base64,{encoded}"
    if args.api == "anthropic":
        with Anthropic(base_url=base_url, api_key=key) as client:
            result = client.messages.create(model=args.model, max_tokens=512, messages=[{
                "role": "user", "content": [
                    {"type": "text", "text": args.prompt},
                    {"type": "image", "source": {"type": "base64", "media_type": media_type, "data": encoded}},
                ],
            }])
        print("".join(block.text for block in result.content if block.type == "text"))
    else:
        with OpenAI(base_url=base_url + "/v1", api_key=key) as client:
            if args.api == "chat":
                result = client.chat.completions.create(model=args.model, messages=[{
                    "role": "user", "content": [
                        {"type": "text", "text": args.prompt},
                        {"type": "image_url", "image_url": {"url": data_uri}},
                    ],
                }])
                print(result.choices[0].message.content)
            else:
                result = client.responses.create(model=args.model, store=False, input=[{
                    "role": "user", "content": [
                        {"type": "input_text", "text": args.prompt},
                        {"type": "input_image", "image_url": data_uri},
                    ],
                }])
                print(result.output_text)


if __name__ == "__main__":
    main()
