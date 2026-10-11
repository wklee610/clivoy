# API and backend compatibility

Clivoy is a Go gateway with OpenAI and Anthropic API frontends. It can run local
Codex, Claude, Gemini and Grok Build CLIs or call a server that exposes OpenAI
Chat Completions and, for image generation, the OpenAI-compatible Images API.
xAI, vLLM, Ollama and LiteLLM connect through that HTTP interface; Clivoy does not
embed an inference engine or require a GPU to start.

```text
OpenAI SDK ──────┐                         ┌─ Codex CLI
Anthropic SDK ───┼─ Clivoy ─ model alias ───┼─ Claude CLI
LiteLLM proxy ───┘                         ├─ Gemini CLI / Grok Build CLI
                                          └─ OpenAI Chat Completions server
                                             (xAI / vLLM / Ollama / LiteLLM)
```

## Supported API surface

Compatibility here describes the endpoints and fields implemented by Clivoy,
not the full OpenAI or Anthropic products. A model alias selects the backend;
the client SDK does not constrain that selection. An Anthropic SDK request can,
for example, use an alias backed by Ollama.

| Capability | API endpoint | Scope |
| --- | --- | --- |
| Model discovery | `GET /v1/models` | Configured aliases, local CLI metadata and discovered Ollama models, with a Clivoy `capabilities` extension |
| OpenAI Chat Completions | `POST /v1/chat/completions` | Text and image input, JSON and SSE, one completion |
| OpenAI Responses | `POST /v1/responses` | Stateless text, image input and function calls, JSON and typed SSE |
| Anthropic Messages | `POST /v1/messages` | Text, image input and tool blocks, JSON and named SSE |
| Image generation | `POST /v1/images/generations` | JSON Images API; requires an HTTP image generation backend |
| Function tools | All three generation endpoints | HTTP backend only; upstream model must support tools |
| Function results | All three generation endpoints | Client executes the function and sends the result with its call ID |
| Usage and finish reasons | All three generation endpoints | Translated from the backend; available detail depends on the backend |
| Health | `GET /healthz` | Public process health; does not prove a CLI is logged in or an upstream is ready |

Responses does not store conversations: send `store=False` and resend the
conversation history in `input` for subsequent turns. `previous_response_id`,
response retrieval, background jobs and persistent conversation APIs are not
implemented. A successful Responses request is not evidence that every
Responses-based agent or coding client will work unchanged.

In streaming Responses, handle `response.incomplete` and `response.failed` as
terminal events as well as `response.completed`. The pinned OpenAI SDK's
`get_final_response()` helper expects `response.completed`; iterate the raw
`responses.create(..., stream=True)` events when handling those other outcomes.

Image edits, image generation streaming, audio, uploaded files, embeddings,
batches, Realtime, provider-hosted tools,
extended thinking and vendor-specific beta features are outside the supported
surface. All CLI backends return text. Codex and Claude accept inline image
input; Gemini and Grok CLI are text-only. Arbitrary SDK tool execution and
sampling controls are not supported by the CLI adapters. The HTTP backend's tool
and sampling behavior still depends on the selected model and upstream server configuration.

For CLI backends, `max_tokens` / `max_output_tokens` is a request in the prompt,
not an enforced token limit. Message history, including system and developer
messages, is serialized into a CLI prompt; it does not guarantee the same role
priority as the vendors' native APIs. Codex streams completed agent messages;
Claude, Gemini and Grok can emit partial text. SSE events are not guaranteed to
correspond to single tokens. CLI-reported usage can also differ from a hosted
API's accounting.
CLI images are attached in order with numbered prompt references; native API
role priorities, exact interleaving and image detail semantics are not guaranteed.
HTTP backends preserve image blocks and can accept HTTP(S) image URLs, which
Clivoy does not fetch. See [image support](images.md) for formats and limits.

The adapters target the command-line options and JSON event formats inspected in
Codex CLI 0.159.2, Claude Code 2.1.91 and Gemini CLI 0.26.0, plus the documented
Grok Build [headless interface](https://docs.x.ai/build/cli/headless-scripting).
Tests use fixture processes rather than
authenticated live CLI calls. Older versions may lack required flags. See the
[provider setup guide](providers.md) for installation and authentication.

## CLI discovery and model selection

When creating a configuration, `init` saves detected CLI absolute paths in
`providers.<name>.command`. Automatic initialization on the first `start` or
`serve` does the same. Existing configurations are not rewritten on upgrade.

When `command` is empty or omitted, CLI discovery checks `PATH` and standard installation
locations, including Codex/ChatGPT app bundles on macOS and supported npm
installations on Windows. Windows npm entrypoints are resolved to a native
executable or Node script without launching a `.cmd` wrapper through a shell.
An explicit provider `command` is authoritative: lookup or launch errors do not
fall back to another installation. Bare `clivoy` shows command help;
`clivoy doctor` shows paths using the same resolver. `doctor` and `models` are
read-only and do not save newly discovered paths. If a saved executable moves,
update `command` or clear it to resume automatic discovery, then restart.
If `init` warns that a CLI is missing, configuration and authentication file
creation can still succeed. Edit the relevant provider's `command` in the
generated configuration to use the actual absolute executable path. Windows paths must escape backslashes in JSON;
see the [setup examples](installation.md#if-a-cli-is-not-detected).

`clivoy models [--config path]` queries the configured providers on every run,
without a generation prompt. `--refresh` is retained with the same behavior.
Without a default configuration it uses built-in configuration defaults and
does not save a configuration or credentials.

| Provider | Metadata query |
| --- | --- |
| Codex | App-server `model/list` |
| Claude | Stream-JSON initialization control response's `models` |
| Gemini | ACP `session/new` response's `models.availableModels` |
| Grok | ACP `_x.ai/models/list` |
| Ollama | HTTP `GET /models` under its configured API prefix |

`SOURCE` distinguishes returned `CLI` or `server` entries from `config` aliases.
Configured aliases are registered names, not verified model access. A failed
query prints a warning and contributes no discovered entries; aliases remain.
Clivoy does not fall back to its own cache or fixed selectors for this command.
The CLI may use its own cached or bundled catalog, so a successful query is not
a guarantee of fresh remote data or account access. Gemini model listing requires
a CLI version that exposes the ACP model metadata; 0.26.0 supports chat but does
not provide that listing and returns an update hint.

Server startup keeps its existing catalog behavior: configured aliases plus
Codex's local `models_cache.json`, Claude's `default`, `opus`, `sonnet`, `haiku`
selectors, Gemini's `auto`, `pro`, `flash`, `flash-lite` selectors, and Ollama's
HTTP model list. The running server keeps this snapshot for `GET /v1/models`.
Restart to update it; `clivoy models` does not reload a running server.

All three conversation APIs accept `<configured-provider-name>/<native-model-id>`
for CLI and Ollama providers, including streaming. Examples are `codex/gpt-6.1-sol`,
`claude/sonnet`, `gemini/flash`, `grok/<model-id>` and `ollama/llama3.2:3b`.
The suffix is passed as the CLI's `--model` value or the Ollama request's `model`.
It must be 1–256 ASCII characters, begin with a letter or digit, and contain only
letters, digits or `_-.:/[]`.
An exact configured alias takes precedence, even if its name has this form.
You can also keep a public alias such as `coding` and set its existing
`provider` and `model` fields to pin the CLI model; no new configuration fields
are required. See [the local example](../examples/clivoy.local.json).

The model list is a selection aid, not an exhaustive inventory or proof of
account access. Unlisted native IDs can be sent directly; the provider determines
whether they work. Generic `openai` backends and `/v1/images/generations` retain
configured-alias routing only. Codex metadata uses the documented
[`model/list` interface](https://learn.chatgpt.com/docs/app-server);
Claude accepts its documented [selectors and full model IDs](https://code.claude.com/docs/en/model-config).
See [Gemini](providers.md#gemini-cli) and [Grok](providers.md#grok-build-cli)
for their selection behavior. Grok's `grok` alias can use its CLI default without
hard-coding a versioned model.

## Ollama model selection

A `type: "ollama"` provider defaults to `http://127.0.0.1:11434/v1`. New
configurations include this provider without choosing a fixed model. Install
and start Ollama separately, then pull a model using the [setup guide](providers.md#ollama).

`clivoy models` and server startup request the configured server's `/models`
endpoint under that API prefix. These are bounded metadata requests: no model
is downloaded or run. Unavailable servers produce warnings and do not prevent
other providers from being used. The server's `GET /v1/models` list is a startup
snapshot; restart Clivoy to update that list after installing another model.
Direct requests such as `ollama/llama3.2:3b` also work for models absent from the
snapshot, provided the Ollama server has them installed.

Discovered and direct Ollama model IDs advertise `chat`. To accept image input,
configure an explicit alias with `capabilities: ["chat", "vision"]` for a vision
model. Function tools, sampling controls and SSE use the HTTP adapter and depend
on the selected model. The Ollama provider does not implement image generation.
Clivoy sends all three conversation API shapes upstream as OpenAI-compatible
Chat Completions; it does not forward Ollama's entire native API.

## Client setup

Start with the [Quickstart](../README.md#quickstart). All `/v1` routes require a
Clivoy gateway key; `/healthz` is public. Use `Authorization: Bearer ...` for
OpenAI requests and `x-api-key: ...` for Anthropic requests. The Anthropic SDK
supplies its version header automatically.

The OpenAI SDK base URL includes `/v1`; the Anthropic SDK base URL is the server
origin. With Clivoy on its default address:

```python
import os
from openai import OpenAI
from anthropic import Anthropic

openai_client = OpenAI(
    base_url="http://127.0.0.1:9610/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
)
anthropic_client = Anthropic(
    base_url="http://127.0.0.1:9610",
    api_key=os.environ["CLIVOY_API_KEY"],
)
```

See [SDK setup and runnable examples](usage.md#official-sdks),
[key management](usage.md#what-is-clivoy_api_key) and
[provider configuration](providers.md). The API behavior is the same under
background `start` and foreground `serve`.

## Connect vLLM, Ollama or LiteLLM as a backend

For Ollama, use `type: "ollama"` to enable model discovery and
`<provider>/<model-id>` selection; see [Ollama setup](providers.md#ollama) and
[the example configuration](../examples/clivoy.ollama.json). Existing Ollama
configurations with `type: "openai"` remain supported through their configured
aliases.

For vLLM or LiteLLM, use `type: "openai"`, the server's API prefix as `base_url`,
and an alias whose `model` is served upstream. See the
[HTTP example](../examples/clivoy.http.json). For authenticated servers, set the
provider's `api_key_env` to an environment variable containing its key.
Keep that key separate from `CLIVOY_API_KEY`, which authenticates clients to
Clivoy. Standalone examples using an environment-only gateway key require
`export CLIVOY_API_KEY="$(clivoy auth)"` before starting the gateway.

| Upstream | Typical local base URL | Configuration notes |
| --- | --- | --- |
| vLLM | `http://127.0.0.1:8000/v1` | Use the model name served by vLLM; tool calling needs a compatible model and vLLM tool parser |
| Ollama | `http://127.0.0.1:11434/v1` | Use a model already installed in Ollama; local Ollama normally needs no API key |
| LiteLLM proxy | `http://127.0.0.1:4000/v1` | Use its configured `model_name` and the proxy's key when authentication is enabled |

These are connection recipes for the shared Chat Completions protocol. The
automated suite uses a fake upstream rather than launching every backend and
model. Verify your deployed backend, model and tool support before relying on
features beyond plain text.

vLLM and Ollama also expose endpoints outside Chat Completions. Clivoy currently
uses `/v1/chat/completions` for its conversation frontends and
`/v1/images/generations` for image generation when the selected backend supports
it. It does not forward entire native APIs. Its Windows, macOS and Linux binary
targets are independent of the platform support of the chosen upstream or CLI.
For example, a Windows Clivoy instance can connect to vLLM running on a separate
Linux server.

## Put LiteLLM in front of Clivoy

LiteLLM can route to Clivoy as an OpenAI-compatible provider. The
[`examples/sdk/litellm-config.yaml`](../examples/sdk/litellm-config.yaml) example
sets `model: openai/codex`, `api_base: http://127.0.0.1:9610/v1` and reads the
gateway token from `CLIVOY_API_KEY` in the LiteLLM process environment. Install
and start LiteLLM separately:

```sh
litellm --config examples/sdk/litellm-config.yaml
```

Then call the LiteLLM proxy with model `clivoy-codex`. If both directions are
configured, avoid routing a Clivoy alias back to itself through LiteLLM.

## Run the SDK compatibility suite

The official Python client versions are pinned in
[`scripts/requirements-sdk.txt`](../scripts/requirements-sdk.txt). The suite
starts a temporary Clivoy process and a local OpenAI-compatible test server on
loopback ports. It uses no real account credentials or model calls.
The pinned clients require Python 3.10 or later.

```sh
go build -o clivoy ./cmd/clivoy
python3 -m venv .venv
.venv/bin/python -m pip install -r scripts/requirements-sdk.txt
.venv/bin/python scripts/sdk_smoke.py --binary ./clivoy
```

On Windows use `go build -o clivoy.exe ./cmd/clivoy`, `py -m venv .venv`, and
`.venv\Scripts\python.exe` in place of `.venv/bin/python`; pass
`--binary .\clivoy.exe`.

The checks cover model discovery, Unicode text, non-streaming calls, raw SDK
streams, SDK stream accumulation, function calls and results across the three
API shapes, token-limit finish reasons, authentication errors, unknown models,
unsupported input and upstream failure. Image checks cover all three input
formats, ordered content, vision streams, capability checks, input validation,
and Images API base64/URL output and metadata. They validate the public HTTP boundary
with SDKs rather than relying only on internal Go types.

## Protocol references

- [Official OpenAI Python SDK reference](https://developers.openai.com/api/reference/python)
- [OpenAI Responses streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events)
- [OpenAI Chat Completions request and stream options](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
- [Anthropic Messages streaming lifecycle](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [vLLM OpenAI-compatible server](https://docs.vllm.ai/en/stable/serving/online_serving/openai_compatible_server/)
- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)
- [LiteLLM OpenAI-compatible endpoints](https://docs.litellm.ai/docs/providers/openai_compatible)
