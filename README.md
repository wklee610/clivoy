<div align="center">

[English](README.md) | [한국어](README-ko.md)

# CLIVOY: CLI + Envoy

**Your AI CLIs, one simple API.**

</div>

Clivoy lets your apps use **Codex, Claude, Gemini, Grok and Ollama** through one
local API. It uses your existing CLI logins and connects to your Ollama server.
You can also connect the **xAI API** or other compatible model servers.
Use an OpenAI or Anthropic SDK, change the base URL, and choose a model.

No GPU or Python is needed to run Clivoy. It is a single Go executable for
macOS, Linux and Windows.

## Installation Guide

From a checkout of this repository, with **Go 1.25 or later** installed:

```sh
go build -o clivoy ./cmd/clivoy
```

On Windows, use `go build -o clivoy.exe ./cmd/clivoy` and replace `./clivoy`
below with `.\clivoy.exe`. If the executable is on your `PATH`, use `clivoy`.

Set up **only the provider you want to use**:

| Provider | Connection | Setup |
| --- | --- | --- |
| Codex | Local Codex CLI | [CLI setup](docs/providers.md#codex-and-claude) |
| Claude | Local Claude Code | [CLI setup](docs/providers.md#codex-and-claude) |
| Gemini | Local Gemini CLI | [Gemini setup](docs/providers.md#gemini-cli) |
| Grok | Local Grok Build CLI; optional xAI API | [Grok setup](docs/providers.md#grok-build-cli) |
| Ollama | Local Ollama server | [Ollama setup](docs/providers.md#ollama) |
| vLLM, LiteLLM | OpenAI-compatible HTTP server | [HTTP setup](docs/providers.md#other-model-servers) |

See the [full installation guide](docs/installation.md) for Windows steps,
CLI detection and package options. Homebrew, APT, release downloads and container
commands require publication to their respective channels; building this checkout
works independently of those releases.

## Quickstart

**1. Start Clivoy.**

```sh
./clivoy start
```

The first start creates your configuration and local API key automatically,
then listens on `0.0.0.0:9610` in the background. Connect locally at
`http://127.0.0.1:9610`, or remotely at `http://<server-ip>:9610`. Existing files are kept.

**2. Load your gateway key.**

```sh
export CLIVOY_API_KEY="$(./clivoy auth)"
```

PowerShell: `$env:CLIVOY_API_KEY = .\clivoy.exe auth`.
This key belongs to your local Clivoy server; it is separate from provider logins
and upstream API keys.
`auth` prints the first active key; `keygen` only generates one. [Register extra keys](docs/usage.md#what-is-clivoy_api_key).

**3. Send a message.**

```sh
curl -sS http://127.0.0.1:9610/v1/chat/completions \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"codex","messages":[{"role":"user","content":"Say hello."}]}'
```

Replace `codex` with `claude`, `gemini` or `grok` for another installed CLI.
[PowerShell request example](docs/installation.md#windows-powershell).

New configurations include all four CLI aliases and a local Ollama connection.
For an existing configuration, [add the providers you need](docs/providers.md#existing-configurations).
For a direct xAI API connection, use the optional [Grok API setup](docs/providers.md#grok-xai-api).

**Using Ollama?** [Install and start Ollama](docs/providers.md#ollama), run
`ollama pull llama3.2:3b`, then use `model: "ollama/llama3.2:3b"` in the request.
You can substitute another model already installed in Ollama.

Try the [short Python example](docs/usage.md#python-connectivity-test): `python3 scripts/test_clivoy.py`.
Install `openai` and choose the model in the file as described in the guide.

## Use your SDK

For an existing Python project with the `openai` package installed:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:9610/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
)
response = client.chat.completions.create(
    model="gemini",
    messages=[{"role": "user", "content": "Explain APIs in one sentence."}],
)
print(response.choices[0].message.content)
```

The Anthropic SDK uses `base_url="http://127.0.0.1:9610"` and the same key.
See [SDK setup and examples](docs/usage.md#official-sdks) for both clients.

## Everyday commands

| Task | Command |
| --- | --- |
| Check the server | `clivoy status` |
| Find installed CLIs | `clivoy doctor` |
| List model names | `clivoy models` |
| Apply configuration changes | `clivoy restart` |
| Stop the server | `clivoy stop` |
| Run in the foreground | `clivoy serve` |

Default files live in `~/.clivoy` (`%USERPROFILE%\.clivoy` on Windows):
`clivoy.json`, `auth.json` and `logs/clivoy.log`. `start` does not enable startup
after reboot. [Full command and configuration guide](docs/usage.md).

## What is supported?

- OpenAI Chat Completions, stateless Responses, Anthropic Messages and SSE.
- Text chat with every provider; image input with Codex, Claude and HTTP vision models.
- Function tools through compatible HTTP backends; image generation through an HTTP image backend.
- Local API keys, request timeouts and a bounded request queue.

CLI requests turn conversation history into a prompt. CLI adapters do not support
API tool calling or sampling controls; Gemini and Grok CLI currently accept text only.
Streaming delivery depends on the backend. Resend conversation history on every
request. This is a subset of the vendor APIs; see the [compatibility guide](docs/compatibility.md).

Local and remote API requests use the same gateway key. Share it with trusted
callers; CLI execution uses your account and is not an OS isolation boundary.
[Security details](SECURITY.md).

## More

- [Provider setup: Gemini, Grok and Ollama](docs/providers.md)
- [Image input and generation](docs/images.md)
- [Configuration examples](examples) · [Release packaging](docs/distribution.md)
- [Contributing and tests](CONTRIBUTING.md) · [Apache-2.0 license](LICENSE)
