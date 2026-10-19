<div align="center">

[English](README.md) | [한국어](README-ko.md)

# CLIVOY: CLI + Envoy

**Your AI CLIs, one simple API.**

</div>

Clivoy lets your apps use **Codex, Claude, Gemini, Grok and Ollama** through one
local API. It uses your existing CLI logins and connects to your Ollama server.
You can also connect the **xAI API** or other compatible model servers.
Use an OpenAI or Anthropic SDK, change the base URL, and choose a model.
Optional Markdown RAG lets the same API answer from your documents or Obsidian notes.

No GPU or Python is needed to run Clivoy. It is a single Go executable for
macOS, Linux and Windows.

## Installation Guide

**macOS and Linux — Homebrew:**

```sh
brew install wklee610/clivoy/clivoy
```

**Go 1.25 or later — macOS, Linux and Windows:**

```sh
go install github.com/wklee610/clivoy/cmd/clivoy@latest
```

Make sure Go's binary directory is on `PATH`: `GOBIN` if set, otherwise
`GOPATH/bin` (usually `~/go/bin` or `%USERPROFILE%\go\bin` on Windows).

**Debian and Ubuntu:** [set up the APT repository](docs/installation.md#apt-debian-and-ubuntu),
then run `sudo apt-get install clivoy`.
For Windows, download a [release archive](https://github.com/wklee610/clivoy/releases/latest).
Alternatively, [build from source](docs/installation.md#build-from-source) with Go 1.25 or later.
Commands below assume `clivoy` is on `PATH`; for a local build, use `./clivoy`
or `.\clivoy.exe` instead.

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
CLI detection and other package options.

## Quickstart

**1. Start Clivoy.**

```sh
clivoy start
```

The first start creates your configuration and local API key automatically,
then listens on `0.0.0.0:9610` in the background. Connect locally at
`http://127.0.0.1:9610`, or remotely at `http://<server-ip>:9610`. Existing files are kept.

**2. Load your gateway key.**

```sh
export CLIVOY_API_KEY="$(clivoy auth)"
```

PowerShell: `$env:CLIVOY_API_KEY = clivoy auth`.
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
They also include an `infra` model for [answers from Markdown documents](#ask-your-documents).
For an existing configuration, [add the providers you need](docs/providers.md#existing-configurations).
For a direct xAI API connection, use the optional [Grok API setup](docs/providers.md#grok-xai-api).

CLI `command` settings accept arrays such as `["codex", "--yolo"]` and
`["claude", "--dangerously-skip-permissions"]`. These options explicitly enable
execution without approval prompts; defaults remain unchanged. See
[CLI options and execution controls](docs/providers.md#codex-and-claude).

**Using Ollama?** [Install and start Ollama](docs/providers.md#ollama), run
`ollama pull llama3.2:3b`, then use `model: "ollama/llama3.2:3b"` in the request.
You can substitute another model already installed in Ollama.

From a source checkout or release archive, try the [short Python example](docs/usage.md#python-connectivity-test): `python3 scripts/test_clivoy.py`.
Install `openai` and choose the model in the file as described in the guide.

## Ask your documents

Starting with v1.1.0, new initialization creates an empty
`~/.clivoy/knowledge/default` directory and an `infra` model backed by Codex.
Add Markdown files, then use `model: "infra"` with the same API and gateway key.
Clivoy searches the files, passes relevant excerpts to the model and appends
their sources to the answer. Document changes refresh every 30 seconds by default.
An empty search returns a no-match answer without calling the model.

Existing configurations stay unchanged. You can explicitly add a knowledge base
pointing to a Markdown folder or Obsidian vault and choose another CLI or HTTP
backend. RAG aliases use separate CLI execution settings; an ordinary model's
`--yolo` option does not carry over. All gateway keys can access all configured
knowledge bases. [Setup, search API and limits](docs/knowledge.md) ·
[Configuration example](examples/knowledge.json).

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
- Optional Markdown RAG, document search and source lists through the same API.
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
- [Markdown and Obsidian RAG](docs/knowledge.md)
- [Configuration examples](examples) · [Release packaging](docs/distribution.md)
- [Contributing and tests](CONTRIBUTING.md) · [Apache-2.0 license](LICENSE)
