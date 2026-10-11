# Image input and generation

Clivoy supports two separate image workflows:

| Workflow | Endpoint | Model capabilities | Backend |
| --- | --- | --- | --- |
| Analyze images and return text | `/v1/chat/completions`, `/v1/responses`, `/v1/messages` | `chat`, `vision` | Codex CLI, Claude Code, or an HTTP vision model |
| Generate images from a text prompt | `/v1/images/generations` | `image_generation` | An HTTP server with an OpenAI-compatible Images API |

Gemini and Grok CLI adapters currently accept text only; use Codex, Claude or
a compatible HTTP vision backend for image input. Ollama vision models need an
explicit alias with `capabilities: ["chat", "vision"]`; discovered and direct
`ollama/<model-id>` names advertise `chat` only. The Ollama provider does not
support image generation. See [Ollama setup](providers.md#ollama).

The capability settings describe what the administrator enables for an alias.
They do not add vision or image generation to an upstream model that lacks it.
`GET /v1/models` returns the configured capabilities as a Clivoy extension to the
OpenAI model object. If `capabilities` is omitted from a model configuration, it
defaults to `["chat"]`. Set both `chat` and `vision` to enable image input.

## Configure models

Run `clivoy init` to create the default `~/.clivoy/clivoy.json` and a random
gateway key in the `keys` array of `~/.clivoy/auth.json` on macOS/Linux. Windows uses
`%USERPROFILE%\.clivoy`; an absolute `CLIVOY_HOME` changes the state root.
Then edit the generated configuration to add your model servers. `init` keeps
the token out of its normal output and refuses to overwrite existing files.
Bare `clivoy start` also initializes a missing default configuration on first
run and starts in the background. `serve` is available for foreground execution;
an explicit `--config` requires an existing file in either mode.

This example exposes a local CLI, an HTTP vision model and an HTTP image
generator, using the generated authentication file. Replace the upstream model names and addresses with your deployment.
Image generation requires the selected server to implement
`/v1/images/generations`; having `/v1/chat/completions` alone is insufficient.

```json
{
  "listen": "0.0.0.0:9610",
  "api_key_env": "CLIVOY_API_KEY",
  "auth_file": "auth.json",
  "request_timeout": "5m",
  "max_concurrent": 8,
  "max_queue": 32,
  "max_body_bytes": 33554432,
  "providers": {
    "codex": {"type": "codex"},
    "models": {
      "type": "openai",
      "base_url": "http://127.0.0.1:8000/v1",
      "api_key_env": "MODEL_SERVER_API_KEY"
    },
    "images": {
      "type": "openai",
      "base_url": "http://127.0.0.1:8001/v1",
      "api_key_env": "IMAGE_SERVER_API_KEY"
    }
  },
  "models": {
    "codex": {"provider": "codex", "capabilities": ["chat", "vision"]},
    "vision-model": {
      "provider": "models",
      "model": "your-vision-model",
      "capabilities": ["chat", "vision"]
    },
    "image-model": {
      "provider": "images",
      "model": "your-image-model",
      "capabilities": ["image_generation"]
    }
  }
}
```

The server reads `auth_file` relative to the configuration file and accepts all
entries in its `keys` array. A nonempty `CLIVOY_API_KEY` overrides the entire
file, accepting only the environment key. `clivoy auth` prints the first active
key for client exports. To add a key, append the output of `clivoy keygen` to
the array manually; restart Clivoy after adding or removing keys. All keys have
the same permissions. Configure any required upstream keys
separately and start with `clivoy start`. Remove a provider's
`api_key_env` when that local server does not require authentication.
Restart Clivoy after changing its configuration. Model
capabilities are checked before the request reaches the backend. An alias with
only `chat` rejects image input, and an alias with only `image_generation`
rejects chat requests.

To keep a separate image configuration, copy
[`examples/clivoy.images.json`](../examples/clivoy.images.json) to a new
`~/.clivoy/clivoy.images.json` (use the Windows or `CLIVOY_HOME` root when
applicable). Set its backend addresses and model names, then stop the old
managed server with `clivoy stop` before starting on the same port (use Ctrl+C
if it was running under `serve`). This example reads the gateway key
from the environment:

```sh
export CLIVOY_API_KEY="$(clivoy auth)"
clivoy start --config "$HOME/.clivoy/clivoy.images.json"
```

The default configuration and key remain unchanged. The optional `log_file`
defaults to the state root's `logs/clivoy.log`; a custom relative path is
configuration-relative. `"-"` disables file output; only foreground `serve`
retains stderr logging. Background `start` has no runtime logs when file output
is disabled. Use `clivoy status`, `clivoy stop` and `clivoy restart` to manage the
background server. Restart retains the recorded configuration path and any
command-line listen override unless you supply replacements.
Runtime logs rotate at 10 MiB with three backups; use separate log paths for
concurrent Clivoy processes.

## Send image input

The three supported API shapes map to the same ordered text-and-image content:

| API | Image content block |
| --- | --- |
| OpenAI Chat Completions | `{"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}` |
| OpenAI Responses | `{"type":"input_image","image_url":"data:image/png;base64,..."}` |
| Anthropic Messages | `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}` |

HTTP backends also accept absolute HTTP(S) image URLs. Use the URL as
`image_url.url` in Chat Completions, `image_url` in Responses, or
`{"type":"image","source":{"type":"url","url":"https://..."}}` in
Anthropic Messages. Clivoy validates and forwards these references without
downloading them; image retrieval and network access occur at the configured
upstream. URLs cannot include username/password credentials.

For HTTP backends, the order of interleaved text and image blocks is preserved.
OpenAI `detail` values `auto`, `low`, `high` and `original` are forwarded; their
meaning and availability depend on the backend. Image input can be used with
the same text-response streaming options as other chat requests.

### CLI input

CLI image input accepts base64 data only. Encode local files on the client;
Clivoy does not accept client-supplied filesystem paths or fetch remote image
URLs for CLI requests.

Codex receives private image files in the operating system's temporary directory
through its image attachment option.
Clivoy removes those files after execution. Claude Code receives image content
through its structured input stream. Image bytes are not placed directly in
command-line arguments. As with text-only CLI requests, conversation history is
converted to a prompt, with numbered image references and attachments in their
original image order. Native API message roles and exact text/image interleaving
cannot be reproduced identically by the CLI prompt format.

The installed CLI and the model selected by that CLI must support vision.
Clivoy's CLI adapters provide image input and return text; they do not provide
the Images generation endpoint.

### Input limits

- Images may appear only in user messages.
- Supported data-URI media types are PNG, JPEG, WebP and GIF. Clivoy checks
  base64 validity and that the byte signature matches the declared media type.
  It does not decode, resize or transcode pixels; the backend must accept the
  image format and contents.
- A request may contain at most 16 images. Each inline image may contain at most
  5 MiB of decoded bytes; inline images together may contain at most 20 MiB.
- The request's JSON body must also fit `max_body_bytes`. Base64 encoding adds
  roughly one third to the binary size. A small existing body limit can therefore
  reject an image before the decoded-image limits apply.
- Remote image byte sizes cannot be checked by Clivoy because it does not fetch
  them. The upstream applies its own limits.
- Uploaded `file_id` references, image edits, masks, and arbitrary file inputs
  are not supported.

## Generate images

Use the OpenAI SDK's Images API with an alias configured for `image_generation`:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:9610/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
)
result = client.images.generate(model="image-model", prompt="A small blue bird", n=1)
for image in result.data or []:
    if image.b64_json:
        print("Received a base64-encoded image")
    elif image.url:
        print(image.url)
```

Clivoy forwards the prompt and supported options to the configured server's
Images API. These include `n`, `size`, `quality`, `response_format`,
`output_format`, `output_compression`, `background`, `moderation`, `style` and
`user`. The upstream decides which option combinations its image model supports;
Clivoy does not synthesize images locally or turn text returned by a CLI into an
image. Options supported by one image model may be rejected by another.

The JSON response preserves upstream `b64_json` or HTTP(S) image URLs, revised
prompts and usage metadata. Clivoy does not download generated image URLs.
Image generation shares the server's authentication, queue, concurrency and
request-timeout controls. The upstream response is limited to 64 MiB.

Only JSON image generation is supported. `stream: true` returns HTTP 400, and
`/v1/images/edits` is unimplemented and returns HTTP 404. Image generation through
Responses hosted tools and through the Anthropic Messages API is not supported.

## Run the examples and tests

Install the SDK dependencies from `scripts/requirements-sdk.txt` and start
Clivoy with your configuration. In the client shell, obtain the generated token:

```sh
export CLIVOY_API_KEY="$(clivoy auth)"
```

In PowerShell, use `$env:CLIVOY_API_KEY = clivoy auth`.
This explicit `auth` command prints the credential, so avoid placing its
output in shared logs. `CLIVOY_BASE_URL` can override the default server origin.

```sh
python examples/sdk/image_example.py analyze photo.png --model codex
python examples/sdk/image_example.py analyze photo.png --model claude --api anthropic
python examples/sdk/image_example.py analyze photo.png --model vision-model --api chat
python examples/sdk/image_example.py generate "A blue bird" --model image-model
```

The example saves a base64 image to a new local file or prints an upstream URL.
`--output` chooses the filename, and an existing file is never overwritten.
These example commands call the configured real backend and can consume its
quota. The automated SDK suite uses only a tiny PNG fixture and a local mock
HTTP server:

```sh
go build -o clivoy ./cmd/clivoy
python scripts/sdk_smoke.py --binary ./clivoy
```

It checks all three image-input formats, content order, streaming text after
image input, capability checks, malformed images, and the Images SDK's JSON
responses, authentication and failure handling. It does not validate real model
vision quality or install an image-generation backend.

## Protocol references

- [OpenAI images and vision](https://developers.openai.com/api/docs/guides/images-vision)
- [OpenAI image generation API](https://developers.openai.com/api/reference/resources/images/methods/generate)
- [Anthropic Messages image input](https://platform.claude.com/docs/en/build-with-claude/working-with-messages)
