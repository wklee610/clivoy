# Provider setup

Clivoy connects one API to the provider you choose. A **provider** describes how
to connect; a **model alias** is the name your client sends, such as `gemini`.
The client SDK does not choose the provider: either SDK can call any chat alias.
Commands below assume `clivoy` is on `PATH`; from a source build, use `./clivoy`
or `.\clivoy.exe` instead.

New configurations include four local CLI aliases:

| Request model | Provider | Authentication | Input |
| --- | --- | --- | --- |
| `codex` | Codex CLI | Existing CLI login | Text and inline images |
| `claude` | Claude Code | Existing CLI login | Text and inline images |
| `gemini` | Gemini CLI | Existing CLI authentication | Text |
| `grok` | Grok Build CLI | Existing CLI authentication | Text |

Install only what you need. Clivoy does not install CLIs or perform interactive
login for you. Start it under the same OS user that logged in to the CLI.
Image support also depends on the selected upstream model.
New configurations also include an [Ollama connection](#ollama) at
`http://127.0.0.1:11434/v1`; choose a model installed on that server.
Run `clivoy models` to query the installed CLIs and Ollama. Its `SOURCE` column
separates their responses from unverified configuration aliases. See
[model selection](usage.md#choose-a-model) for failures and catalog limits.

## Codex and Claude

Install and authenticate using the provider's official instructions:

- [Codex CLI](https://developers.openai.com/codex/cli/)
- [Claude Code](https://code.claude.com/docs/en/overview)

The default aliases are `codex` and `claude`. An alias without a `model` field
lets the CLI select its model. Codex runs with user configuration ignored; use
a specific model when you need one. For example, `claude/sonnet` selects that
CLI's Sonnet selector. `clivoy models` queries each CLI's metadata interface.

To supply CLI options, make `command` an array in `clivoy.json`. Its first entry
is the executable (keep the absolute path saved by `init` if available), followed
by one entry per argument. For example, these provider entries opt into execution
without approval prompts:

```json
{
  "codex": {"type": "codex", "command": ["codex", "--yolo"]},
  "claude": {"type": "claude", "command": ["claude", "--dangerously-skip-permissions"]}
}
```

Merge these entries into `providers`, then run `clivoy restart`. Codex also
accepts `--dangerously-bypass-approvals-and-sandbox` instead of `--yolo`. Those
Codex options remove Clivoy's read-only sandbox and fixed approval setting;
Claude's option removes its empty tool list and fixed `dontAsk` permission mode.
This permits the CLI's own tools to act with the server user's permissions.
The default configuration does not add these options: Codex remains read-only
and Claude runs without tools. Required JSON output and request/session controls
remain configured by Clivoy. CLI tool activity is internal to the provider;
it does not add API function-tool support. See [execution controls](../SECURITY.md).

## Gemini CLI

Install the official CLI, then run it once to authenticate:

```sh
npm install -g @google/gemini-cli
gemini
```

Follow [Gemini's installation guide](https://geminicli.com/docs/get-started/installation/)
for runtime requirements and other package options, and its
[authentication guide](https://geminicli.com/docs/get-started/authentication/)
for Google login, Gemini API keys or Vertex AI. Clivoy reuses the CLI's available
credentials. If your chosen authentication uses environment variables, provide
them in the shell that starts or restarts Clivoy.

Use `model: "gemini"`, or a native selector such as `gemini/flash`.
Model listing requires a Gemini version that exposes ACP model metadata.
Older versions such as 0.26.0 can serve chat but need an update for `clivoy models`.
Actual model availability depends on your installed CLI and account.
See [Gemini model selection](https://geminicli.com/docs/cli/model/).

The Gemini adapter supports text conversations and incremental SSE output.
Image input, API function tools and sampling controls are not supported by this
adapter. Conversations are sent as a prompt on stdin. Clivoy disables tools,
extensions, hooks and project context while retaining the CLI's authentication
and model settings. Gemini's own caches and session logs remain subject to its
retention settings; temporary Clivoy files are cleaned up. See
[compatibility](compatibility.md) and [execution controls](../SECURITY.md).

## Grok Build CLI

Install the official [Grok Build CLI](https://docs.x.ai/build/overview) using the
instructions for your OS, then run `grok` once to authenticate. The official CLI
supports browser authentication and API-key authentication. Clivoy keeps each
request's configuration separate and points `GROK_AUTH_PATH` to the native login
file. Grok retains its normal login locking and token refresh. An existing
`GROK_AUTH_PATH` is respected; otherwise the file is `auth.json` under the original
`GROK_HOME` or `~/.grok`. API-key authentication uses `XAI_API_KEY` from the server
environment. Custom authentication commands and API keys stored only in personal
CLI configuration are not imported.

Use `model: "grok"` for the built-in CLI default. A native model ID can also be
selected as `grok/<model-id>`; its availability is determined by the installed
CLI and account. Custom models from your personal Grok configuration are not
imported; configure a separate HTTP provider for custom endpoints. This adapter
currently supports text and incremental SSE output. Every request uses a fresh
working directory; `work_dir` is not supported for Grok. It does not expose CLI
tools through the API. See [compatibility](compatibility.md) and
[execution controls](../SECURITY.md).

Grok installations with `managed_config.toml` or `requirements.toml` in the
original Grok home or `/etc/grok` are not supported by the isolated CLI adapter.
Use the [xAI HTTP connection](#grok-xai-api) for those deployments.

## Existing configurations

Upgrading the executable does not replace an existing `clivoy.json`. Merge the
entries you need into its existing `providers` and `models` objects:

```json
{
  "providers": {
    "gemini": {"type": "gemini"},
    "grok": {"type": "grok"},
    "ollama": {"type": "ollama"}
  },
  "models": {
    "gemini": {"provider": "gemini", "capabilities": ["chat"]},
    "grok": {"provider": "grok", "capabilities": ["chat"]}
  }
}
```

This is a fragment: keep your other settings, providers, models and `auth_file`.
Add only the provider you want, then run `clivoy restart`. Ollama needs only its
provider entry: model names use `ollama/<installed-model-id>`.
If executable discovery fails, see [the installation guide](installation.md#if-a-cli-is-not-detected).

## Grok xAI API

To connect directly to the xAI API, add a separate HTTP provider and alias.
This is optional and is not part of the default configuration.
Merge the following entries into `clivoy.json`, keeping its existing fields:

```json
{
  "providers": {
    "xai": {
      "type": "openai",
      "base_url": "https://api.x.ai/v1",
      "api_key_env": "XAI_API_KEY"
    }
  },
  "models": {
    "grok-api": {
      "provider": "xai",
      "model": "grok-4.7",
      "capabilities": ["chat", "vision"]
    }
  }
}
```

Set your xAI API key in the terminal that starts the gateway, then restart:

```sh
export XAI_API_KEY="your-xai-api-key"
clivoy restart
```

PowerShell:

```powershell
$env:XAI_API_KEY = "your-xai-api-key"
clivoy restart
```

Send `model: "grok-api"` from your client. `CLIVOY_API_KEY` authenticates the
client to Clivoy; `XAI_API_KEY` authenticates Clivoy to xAI. Keep these separate.
The configured environment variable must be present every time the server starts
or restarts. Requests use your xAI API account's access, billing and limits.

The adapter calls xAI's OpenAI-compatible Chat Completions endpoint for all
three conversation APIs, including Clivoy's Responses endpoint. This HTTP route
supports image input and client function tools when the upstream model does. It does not
forward xAI's full Responses API or hosted tools. Model and parameter support
are determined by xAI. See the [official xAI API documentation](https://docs.x.ai/overview)
and [example configuration](../examples/clivoy.grok.json). That standalone example
reads the gateway key from `CLIVOY_API_KEY`; load it with `clivoy auth` before
starting the example configuration.

## Ollama

[Install Ollama](https://docs.ollama.com/quickstart) and start its app or server.
If the server is not already running, use `ollama serve` in a separate terminal.
Download a model you want to use; for example, this
[Llama 3.2 model](https://ollama.com/library/llama3.2:3b):

```sh
ollama pull llama3.2:3b
clivoy start
```

Send `model: "ollama/llama3.2:3b"` from any supported SDK or HTTP endpoint.
Replace `llama3.2:3b` with another installed model name, including its tag.
The prefix `ollama` is the configured provider name. New Clivoy configurations
already include `"ollama": {"type": "ollama"}`; existing configurations need
that [provider entry added once](#existing-configurations).

Clivoy connects to Ollama's HTTP API at `http://127.0.0.1:11434/v1`. It does not
install Ollama, download models or start the Ollama server. Local Ollama models
need no upstream API key; clients still need the Clivoy gateway key. Model memory
and hardware requirements belong to Ollama and the selected model.

To use another Ollama server, set `base_url` on its provider to the server's API
prefix, including `/v1`. Set `api_key_env` if that deployment requires a key.
These are server configuration fields, not client request parameters.

`clivoy models` lists models reported by the configured Ollama server. Discovery
also runs when Clivoy starts, using a bounded metadata request. An unavailable
Ollama server produces a warning without preventing other providers from working.
After pulling a new model, use its full `ollama/<model-id>` immediately, or restart
Clivoy to update the server's `GET /v1/models` catalog.

An optional explicit alias can provide a shorter request name; see
[the Ollama example](../examples/clivoy.ollama.json). For image input, configure
an alias with `capabilities: ["chat", "vision"]` and select an installed vision
model; discovered and direct Ollama model names advertise only `chat`. Tools and
sampling options use the HTTP backend's support and depend on the chosen model.
The Ollama provider does not support image generation.
[Ollama's OpenAI compatibility reference](https://docs.ollama.com/api/openai-compatibility).

## Other model servers

Add a provider with `type: "openai"`, the server's API prefix as `base_url`, and
a model alias whose `model` matches an installed or served upstream model.
Use `api_key_env` on the provider if that server requires its own API key.

| Server | Common local API prefix | Example |
| --- | --- | --- |
| vLLM | `http://127.0.0.1:8000/v1` | [HTTP configuration](../examples/clivoy.http.json) |
| LiteLLM | `http://127.0.0.1:4000/v1` | [HTTP configuration](../examples/clivoy.http.json) |

Keep the `auth_file` from initialization when editing your default configuration.
Standalone examples that use only `api_key_env: "CLIVOY_API_KEY"` need that key
in the server environment, for example `export CLIVOY_API_KEY="$(clivoy auth)"`.
Restart after changing configuration. [Detailed HTTP integration](compatibility.md#connect-vllm-ollama-or-litellm-as-a-backend).

## Configuration reference

CLI providers accept an optional `command` array containing an executable and
literal arguments, such as `["/path with spaces/codex", "--yolo"]`. Arguments
are never shell-expanded or split on spaces: use separate array entries for
separate flags and values. The executable must be nonempty and not whitespace-only;
all entries must be strings without NUL characters. Omit `command`, or set it to
`[]` or `null`, to discover the executable automatically. Legacy executable-only
strings are still accepted (including `""` for discovery), but strings containing
flags are not split. New initialization writes arrays. Metadata discovery and
`doctor` use only the executable; generation arguments are not passed to model
listing. Clivoy adds its own protocol options, so avoid conflicting output,
session or prompt flags.

Codex, Claude and Gemini also accept `work_dir`; otherwise each request uses a fresh temporary working
directory, removed after execution. Grok always uses a fresh working directory
and rejects `work_dir` configuration. These paths and upstream URLs are set by
the administrator, never by API callers.

If model `capabilities` is omitted, it defaults to `["chat"]`. Add `vision` only
for a supported vision backend, or use `["image_generation"]` for an HTTP image
backend. Capabilities declare what the backend supports; they do not add features.
[Image setup and examples](images.md).

The defaults permit eight concurrent requests and queue 32 more across all models.
Overload returns HTTP 429. The five-minute timeout includes queue time. Configure
`max_concurrent`, `max_queue` and `request_timeout` to fit your backend; existing
files keep their saved values. `max_body_bytes` defaults to 32 MiB.
