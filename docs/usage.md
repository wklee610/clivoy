# Usage guide

Start with the [Quickstart](../README.md#quickstart). This guide covers daily
commands, model selection, configuration files, credentials and SDK examples.
For backend setup, see the [provider guide](providers.md).

## Command reference

These examples use the binary built in the repository: `./clivoy` on macOS/Linux
or `.\clivoy.exe` in PowerShell. If Clivoy is installed on your `PATH`, use
`clivoy` instead. Build and Python example paths below are relative to the
repository root; the default configuration is independent of your current directory.

| Task | Command |
| --- | --- |
| Show command help | `./clivoy` |
| Show commands and flags | `./clivoy --help` |
| Show version and build information | `./clivoy version` |
| Initialize the default user directory | `./clivoy init` |
| Create a separate configuration and authentication file | `./clivoy init --output clivoy.local.json` |
| Show configured CLI paths and check the server key | `./clivoy doctor` |
| Query CLI and Ollama model lists, alongside configured aliases | `./clivoy models` |
| Same model query; retained for compatibility | `./clivoy models --refresh` |
| Print the first active key for client use | `./clivoy auth` |
| Generate a key without saving or registering it | `./clivoy keygen` |
| Start in the background | `./clivoy start` |
| Check the managed server | `./clivoy status` |
| Stop the managed server | `./clivoy stop` |
| Restart the managed server | `./clivoy restart` |
| Start with another configuration | `./clivoy start --config clivoy.local.json` |
| Start on another port | `./clivoy start --listen 127.0.0.1:9090` |
| Run in the foreground | `./clivoy serve` |

`init --output clivoy.local.json` also creates `auth.json` in the same directory;
use `--config clivoy.local.json` to run that configuration. Neither file is
overwritten; choose a separate directory if it already contains `auth.json`.
`keygen` prints a key without saving it or changing the server's accepted keys.
When using port `9090`, adjust
the client URLs below, or set `CLIVOY_BASE_URL=http://127.0.0.1:9090` for the
Python example scripts.

## Choose a model

```sh
./clivoy models
./clivoy models --config clivoy.local.json
./clivoy models --refresh
```

`clivoy models` queries the configured CLIs and Ollama servers each time, without
sending a generation prompt. `--refresh` performs the same query. It also works
before `init`, using the default configuration without saving files.
The `SOURCE` column explains each entry:

| Source | Meaning |
| --- | --- |
| `CLI` | Returned by the installed CLI's model metadata interface |
| `server` | Returned by the configured Ollama server |
| `config` | A configured alias; its model or login has not been verified |

If a query fails, Clivoy prints a warning and retains the configured aliases.
It does not substitute its own cached models or fixed selectors for that query.
The CLI itself may return cached or bundled metadata, so the result does not
prove current account access. Gemini requires a version that exposes model
metadata through ACP; older versions such as 0.26.0 need an update for listing.
See [discovery compatibility](compatibility.md#cli-discovery-and-model-selection).

The running server's `GET /v1/models` remains a startup snapshot built from
configured aliases, local CLI metadata/selectors and Ollama's model list.
This command does not reload it; restart after changing models or configuration
to update that endpoint. You can still request an unlisted native model directly.

For Chat Completions, Responses and Messages, set `model` to
`<configured-provider-name>/<native-model-id>`, for example:

| Request model | Selected model |
| --- | --- |
| `codex/gpt-6.1-sol` | Codex with `--model gpt-6.1-sol` |
| `claude/sonnet` | Claude's Sonnet selector |
| `claude/opus` | Claude's Opus selector |
| `claude/claude-sonnet-4-6` | A specific Claude model ID |
| `gemini/flash` | Gemini's Flash selector |
| `grok/<model-id>` | A native model supported by Grok Build |
| `ollama/llama3.2:3b` | The `llama3.2:3b` model installed in Ollama |

The prefix is the name under `providers`, which may differ from its type.
An exact configured alias always takes precedence. IDs absent from the list
can still be passed directly; the list is neither exhaustive nor a guarantee
of account access. The CLI account or Ollama server decides availability. Clivoy does not download
missing Ollama models. Generic `openai` HTTP backends and image generation
continue to use configured aliases only.
To keep a stable public alias, set its existing `provider` and `model` fields;
see [the local configuration example](../examples/clivoy.local.json).
See [Gemini](providers.md#gemini-cli) and [Grok](providers.md#grok-build-cli)
setup for their available selectors and authentication.
[Ollama setup](providers.md#ollama) covers server startup, model downloads and
optional aliases for vision models.

## Configuration, logs and temporary files

New configurations listen on `0.0.0.0:9610`, accepting connections on all IPv4
interfaces. Use `http://127.0.0.1:9610` locally or `http://<server-ip>:9610` from
another computer. Both use the same gateway key. Use the server's reachable IP
in client URLs; `0.0.0.0` is the listen address.

Existing configurations keep their saved `listen` value. Edit `clivoy.json` and
restart to change it, or use `clivoy restart --listen 0.0.0.0:9610`. To accept
only local connections, use `127.0.0.1:9610` as the listen address instead.

Clivoy keeps its default state together under your home directory:

| File or directory | macOS/Linux default |
| --- | --- |
| Configuration | `~/.clivoy/clivoy.json` |
| Server API keys | `~/.clivoy/auth.json` |
| Runtime log | `~/.clivoy/logs/clivoy.log` |
| Markdown documents created by new initialization | `~/.clivoy/knowledge/default/` |
| Managed daemon state and private control token | `~/.clivoy/daemon.json` |
| Daemon coordination locks | `~/.clivoy/daemon.lock`, `~/.clivoy/daemon-command.lock` |

CLI request working directories and Codex image attachments use the operating
system's temporary directory on demand and are removed after execution.
Clivoy does not create a `tmp` directory under its state root.
`daemon.lock` prevents duplicate servers; `daemon-command.lock` serializes
start, stop and restart commands. These files remain after shutdown, but the
operating system releases their locks when the process exits. Their presence
does not mean the server is running; use `clivoy status`. Do not delete them
while Clivoy commands or its daemon are running.

On Windows the same layout starts at `%USERPROFILE%\.clivoy`. Set `CLIVOY_HOME`
to an **absolute directory path** to change the state root, and use the same
value in server management and token-retrieval commands. Leave it unset to use
the default; an empty or relative value is rejected. This optional example
starts a separate instance on port `9090`:

```sh
CLIVOY_HOME="$HOME/.clivoy-work" clivoy init
CLIVOY_HOME="$HOME/.clivoy-work" clivoy start --listen 127.0.0.1:9090
CLIVOY_HOME="$HOME/.clivoy-work" clivoy status
```

```powershell
$previousClivoyHome = $env:CLIVOY_HOME
try {
    $env:CLIVOY_HOME = Join-Path $env:USERPROFILE ".clivoy-work"
    clivoy init
    clivoy start --listen 127.0.0.1:9090
    clivoy status
} finally {
    $env:CLIVOY_HOME = $previousClivoyHome
}
```

Retrieve this instance's token or stop it with the same `CLIVOY_HOME` override;
its clients use `http://127.0.0.1:9090`. The examples above leave the terminal's
default state-root setting unchanged.

Without `--config`, `start`, `serve`, `doctor` and `auth` use the default
`clivoy.json` under that root. `doctor` and `auth` do not initialize a
missing configuration; they tell you to run `clivoy init`. `start` and `serve`
without an explicit configuration perform first-run initialization. `--config` selects
an existing configuration and never initializes it automatically; `init --output`
creates a configuration and adjacent `auth.json` at your chosen path. Relative flag
paths are resolved from the current directory. `auth_file` stays relative to
its configuration's directory.

New initialization adds a `default` knowledge base rooted at `knowledge/default`
beside the configuration, and a text-only `infra` model backed by Codex. The
document directory starts empty. Existing configurations are not changed; add
`knowledge_bases` and a model's `knowledge` explicitly to enable retrieval.
Document edits refresh automatically, while configuration edits require a
restart. [Markdown/Obsidian setup and search API](knowledge.md).

New initialization also saves detected CLI absolute paths as one-element arrays in
`providers.<name>.command` for later runs. Add optional CLI arguments as separate
array entries, for example `["codex", "--yolo"]`; see [CLI options and execution
controls](providers.md#codex-and-claude). Legacy executable strings still work.
Missing CLIs keep that field omitted
and use automatic discovery. Existing configurations are not rewritten; `doctor`
and `models` are read-only. If a saved executable moves, update its `command` or
clear it to resume automatic discovery, then restart. See [manual path setup](installation.md#if-a-cli-is-not-detected).

The optional `log_file` setting defaults to the state root's `logs/clivoy.log`.
A custom relative path is resolved from the configuration directory; `"log_file":
"-"` disables the file. Foreground `serve` still logs to stderr; a detached
`start` discards its standard streams, so disabling the file leaves no background
runtime log. Startup errors are returned to the initiating command separately.
Managed directories use mode `0700` and files use `0600` on macOS/Linux.
Windows access follows the
user directory's ACLs. Existing state directories with group/other permissions
or symlinks are rejected instead of silently changing them; follow the reported
`chmod 700` guidance for directories you own. Temporary request files are
removed after execution. Provider CLI credentials and caches remain in those
programs' own locations; Clivoy does not migrate their login state.

The runtime log rotates at 10 MiB and keeps three backup files. Multiple Clivoy
processes must use separate `log_file` paths; concurrent writes to the same log
file are not supported.

An older configuration in your working directory still works with
`clivoy serve --config /path/to/old/clivoy.json`; use the same `--config` with
`doctor` or `auth`. Clivoy does not move or overwrite old configurations
automatically.

## Manage the background server

Use the built-in commands to run Clivoy independently of the terminal:

```sh
clivoy start
clivoy status
```

`clivoy stop` requests graceful shutdown and waits for completion. It reports
an error if shutdown times out. `clivoy restart` reuses the recorded configuration
path and command-line `--listen` override for options you omit, including a
record left by an exited instance. Without a recorded instance it behaves like
`start`. If there was no `--listen` override, the configuration's `listen` value
is read again on restart.
Both `start` and `restart` accept `--config` and `--listen`. `stop` and `status`
take no options and find the instance through the current `CLIVOY_HOME`.

Each state root manages one background instance. Use another `CLIVOY_HOME`, a
different listen address and a separate log path for additional instances.
Keep `daemon.json` private: it contains the local control token, and uses mode
`0600` on macOS/Linux. The daemon runs as the same user and retains access to
that user's existing CLI login. These commands do not install a system service
or enable automatic startup after reboot.

`serve` remains a foreground command for terminals, containers and external
process supervisors; stop that process with Ctrl+C rather than `clivoy stop`.

## What is CLIVOY_API_KEY?

It is the password for **your Clivoy gateway**, separate from provider logins
and upstream API keys. No Clivoy account or key-issuance website is needed.

- `clivoy auth` prints the first active key for client exports. It does not list
  all registered keys.
- `clivoy keygen` generates and prints a new key. It does not save or register it,
  so running it alone does not change what `auth` returns or what the server accepts.

`init` creates the first key in `auth.json`, referenced by
`"auth_file": "auth.json"` in the configuration. To register a generated key,
append the `keygen` output as a string in that file's `keys` array, then run
`clivoy restart`. Remove an entry and restart to revoke it. Foreground servers
must be stopped and started again. All registered keys have the same access.

A nonempty **server-side** `CLIVOY_API_KEY` overrides the entire file: the server
accepts only that environment key. Leave it unset when starting or restarting
the server to use all file keys. `auth` also returns this override when the
variable is set in its own environment; otherwise it prints the first file key.

In the client terminal, load that key into the environment:

```sh
export CLIVOY_API_KEY="$(./clivoy auth)"
```

```powershell
$env:CLIVOY_API_KEY = .\clivoy.exe auth
```

With file authentication, clients may use any registered key. Startup and `doctor` never print keys.
Keep `auth.json` private and out of version control; `init` creates it with mode
`0600` on macOS/Linux. Relative `auth_file` paths are resolved from the
configuration's directory.

Legacy `api_key_file` configurations still read a plain-text key file such as
`clivoy.json.key`. New initialization creates `auth.json` instead; existing
credentials are not automatically migrated or replaced.

## HTTP examples

With the server running and the token loaded in your client terminal, check
health and list known models:

```sh
curl -sS http://127.0.0.1:9610/healthz
curl -sS http://127.0.0.1:9610/v1/models \
  -H "Authorization: Bearer $CLIVOY_API_KEY"
```

Use an entry's `id` as the `model` in your requests. Its `capabilities` indicate
whether it supports `chat`, image input through `vision`, or `image_generation`.

Chat Completions:

```sh
curl -sS http://127.0.0.1:9610/v1/chat/completions \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"codex/gpt-6.1-sol","messages":[{"role":"user","content":"Say hello."}]}'
```

Stream the answer as SSE events:

```sh
curl -N http://127.0.0.1:9610/v1/chat/completions \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"codex","stream":true,"messages":[{"role":"user","content":"Explain HTTP streaming briefly."}]}'
```

Responses:

```sh
curl -sS http://127.0.0.1:9610/v1/responses \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"codex","input":"Say hello.","store":false}'
```

Anthropic Messages:

```sh
curl -sS http://127.0.0.1:9610/v1/messages \
  -H "x-api-key: $CLIVOY_API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude","max_tokens":512,"messages":[{"role":"user","content":"Say hello."}]}'
```

All `/v1` routes require a server key. `GET /healthz` is the public liveness
endpoint. In PowerShell, use `Invoke-RestMethod` to list models and send JSON:

```powershell
$headers = @{ Authorization = "Bearer $env:CLIVOY_API_KEY" }
Invoke-RestMethod -Uri "http://127.0.0.1:9610/v1/models" -Headers $headers

$body = @{
    model = "codex"
    messages = @(@{ role = "user"; content = "Say hello." })
} | ConvertTo-Json -Depth 6
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:9610/v1/chat/completions" `
    -Headers $headers -ContentType "application/json" -Body ([System.Text.Encoding]::UTF8.GetBytes($body))
```

## Python connectivity test

`scripts/test_clivoy.py` is a short example using the official OpenAI Python SDK.
Use Python 3.10+ in a [virtual environment](#official-sdks). With Clivoy running,
run from the repository root:

```sh
python3 -m pip install openai
export CLIVOY_API_KEY="$(./clivoy auth)"
python3 scripts/test_clivoy.py
```

For a `PATH` installation, use `clivoy auth`. Edit `model=` in the script to use
`codex`, `claude/sonnet`, `gemini/flash`, `grok`, or `ollama/<installed-model>`.
Its initial `ollama/gpt-oss:20b` value requires that model to be installed in your
running Ollama server; change it to match your setup. The example makes one real
model request. For another server, edit its `base_url=` as well.

## Official SDKs

Use Python 3.10 or later to create an environment and install the SDK versions
used by the examples.
These are client dependencies, not Clivoy server dependencies. Keep the server
running and load `CLIVOY_API_KEY` in this client terminal as shown above.

macOS/Linux:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r scripts/requirements-sdk.txt
python examples/sdk/openai_example.py
python examples/sdk/anthropic_example.py
```

PowerShell:

```powershell
py -3 -m venv .venv
.\.venv\Scripts\python.exe -m pip install -r scripts/requirements-sdk.txt
.\.venv\Scripts\python.exe examples/sdk/openai_example.py
.\.venv\Scripts\python.exe examples/sdk/anthropic_example.py
```

PowerShell uses the environment's interpreter directly, so activation is
unnecessary. Use `.\.venv\Scripts\python.exe` in place of `python` in the
remaining Python commands on Windows.

The OpenAI example calls the `codex` alias; the Anthropic example calls `claude`.
Set `CLIVOY_MODEL` to another alias or CLI model ID to change either example. Both
scripts use `CLIVOY_BASE_URL` when set, with `http://127.0.0.1:9610` as the default.
The URL should have no `/v1` suffix; the scripts add the appropriate API path.

For example, after configuring a `local-model` alias and starting Clivoy on
port `9090`:

```sh
CLIVOY_MODEL="local-model" CLIVOY_BASE_URL="http://127.0.0.1:9090" \
  python examples/sdk/openai_example.py
```

```powershell
$env:CLIVOY_MODEL = "local-model"
$env:CLIVOY_BASE_URL = "http://127.0.0.1:9090"
.\.venv\Scripts\python.exe examples/sdk/openai_example.py
Remove-Item Env:CLIVOY_MODEL, Env:CLIVOY_BASE_URL
```

To use the SDKs directly in your own code:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:9610/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
)
response = client.chat.completions.create(
    model="codex/gpt-6.1-sol",
    messages=[{"role": "user", "content": "Explain HTTP streaming briefly."}],
)
print(response.choices[0].message.content)

response = client.responses.create(
    model="codex", input="Say hello.", store=False,
)
print(response.output_text)
```

```python
import os
from anthropic import Anthropic

client = Anthropic(
    base_url="http://127.0.0.1:9610",  # This SDK adds /v1/messages.
    api_key=os.environ["CLIVOY_API_KEY"],
)
with client.messages.stream(
    model="claude/sonnet",
    max_tokens=512,
    messages=[{"role": "user", "content": "Say hello."}],
) as stream:
    for text in stream.text_stream:
        print(text, end="", flush=True)
```

The Anthropic `max_tokens` field is required by its API. For CLI backends it is
an instruction in the prompt, **not an enforced token cap**. HTTP backends receive
the option directly. See [runnable SDK examples](../examples/sdk) and the
[compatibility matrix](compatibility.md) for exact behavior and limitations.
