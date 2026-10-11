# Installation guide

Build from a local checkout, then set up the AI CLI or model server you want to
use. Clivoy itself needs no GPU, Python runtime or third-party Go dependencies.
The provider's own runtime and account requirements still apply.

## Build from source

Install Go 1.25 or later. In the repository root:

```sh
go build -o clivoy ./cmd/clivoy
./clivoy version
```

On Windows, use `go build -o clivoy.exe ./cmd/clivoy` and `.\clivoy.exe version`.
The examples below use this local executable. If you place it in a directory on
`PATH`, use `clivoy` from any directory.

Next, [set up one provider](providers.md): Codex, Claude Code, Gemini CLI, Grok
Build or Ollama. Clivoy uses the login of the user who starts a CLI. You do not
have to install all four CLIs. For Ollama, start its local server and download a
model using the [Ollama setup](providers.md#ollama); new Clivoy configurations
already include the local connection. The xAI API needs a key and explicit setup.

## macOS and Linux

From the repository root:

```sh
./clivoy start
export CLIVOY_API_KEY="$(./clivoy auth)"
curl -sS http://127.0.0.1:9610/v1/chat/completions \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"codex","messages":[{"role":"user","content":"Say hello."}]}'
```

Use `claude`, `gemini` or `grok` in place of `codex` for another installed CLI,
or `ollama/llama3.2:3b` after preparing that model in Ollama.
The request calls the selected provider and uses that account's quota.

## Windows PowerShell

In the repository root:

```powershell
go build -o clivoy.exe ./cmd/clivoy
.\clivoy.exe start
$env:CLIVOY_API_KEY = .\clivoy.exe auth

$headers = @{ Authorization = "Bearer $env:CLIVOY_API_KEY" }
$body = @{
    model = "codex"
    messages = @(@{ role = "user"; content = "Say hello." })
} | ConvertTo-Json -Depth 6
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:9610/v1/chat/completions" `
    -Headers $headers -ContentType "application/json" -Body ([System.Text.Encoding]::UTF8.GetBytes($body))
```

Use `claude`, `gemini` or `grok` in place of `codex` for another installed CLI,
or `ollama/llama3.2:3b` after preparing that model in Ollama.
Use `.\clivoy.exe status` and `.\clivoy.exe stop` to manage the server.

## First-run behavior

`start` creates the default configuration and a random local API key when they
are missing, then starts a background server listening on `0.0.0.0:9610`.
Use `http://127.0.0.1:9610` locally or `http://<server-ip>:9610` from another
computer, with the same gateway key.
It returns after the server is ready. Existing files are never overwritten.
You can close the terminal without stopping the server. This does not install a
system service or enable automatic startup after reboot.

Existing configurations keep their saved `listen` address. To change it, edit
`clivoy.json` and run `clivoy restart`, or set an explicit `--listen` override.
When creating a configuration, `init` saves the absolute paths of detected CLIs
in `providers.<name>.command`. First-run `start` and `serve` do the same through
automatic initialization. Later runs reuse those paths; existing configurations
are not rewritten automatically.

The default state directory is `~/.clivoy` on macOS/Linux or
`%USERPROFILE%\.clivoy` on Windows. The main files are:

| File | Purpose |
| --- | --- |
| `clivoy.json` | Providers, model aliases and server settings |
| `auth.json` | Local gateway keys |
| `logs/clivoy.log` | Runtime diagnostics |

Run as your normal user so Clivoy can use that user's CLI login. `clivoy auth`
prints the first accepted gateway key; it does not log in to an AI provider.
For alternate paths, key rotation and foreground execution, see [usage](usage.md).

## If a CLI is not detected

```sh
./clivoy doctor
```

`doctor` checks executable discovery and gateway-key configuration. It does not
test provider login, connectivity or model access. A missing CLI warning during
first start does not prevent initialization; only the provider you call must work.

When a provider's `command` is empty or omitted, Clivoy searches `PATH` and known
installation locations, including supported macOS app bundles and Windows npm
installations. If a CLI is missing during initialization, its `command` stays
omitted so discovery can find it after installation. `doctor` and `models` only
inspect configuration; they do not save newly discovered paths.

To set a path manually, edit `providers.<name>.command` in
`~/.clivoy/clivoy.json` (`%USERPROFILE%\.clivoy\clivoy.json` on Windows).
Use the executable's absolute path. For example, set `providers.gemini` to:

```json
{"type": "gemini", "command": "/absolute/path/to/gemini"}
```

Keep the rest of the configuration. Windows JSON paths escape backslashes, for example
`"command": "C:\\path\\to\\grok.exe"`. An explicit `command` takes precedence
and does not fall back to another installation on failure. If the executable
moves, update `command` or clear it to resume automatic discovery. Run `./clivoy doctor`
to verify the path, then `./clivoy restart` to apply it. See
[CLI discovery](compatibility.md#cli-discovery-and-model-selection) for details.

## Package installation

The repository contains packaging for the channels below. **These commands work
only after the source or release has been published to that channel.** Their
presence here does not confirm that a public package is currently available.
Use the source build above for an unpublished checkout.

| Channel | Installation after publication |
| --- | --- |
| Homebrew, macOS/Linux | `brew install wklee610/clivoy/clivoy` |
| Go module | `go install github.com/wklee610/clivoy/cmd/clivoy@latest` |
| GitHub Releases | Download your OS/architecture archive and verify `checksums.txt` |
| APT, Debian/Ubuntu | Add the signed repository, then `sudo apt-get install clivoy` |
| Container | `docker pull ghcr.io/wklee610/clivoy:latest` |

Native packaging targets Windows, macOS and Linux on x86-64 and ARM64.
Clivoy's platform support does not change the selected provider's requirements.
The container includes Clivoy and CA certificates; CLI programs and their login
state are not bundled. Use it with configured HTTP backends.

[Distribution instructions](distribution.md) cover signature/checksum checks,
container setup, local Homebrew testing and release prerequisites. After any
installation, run `clivoy start` as your own user; package installation does not
create per-user configuration or start the gateway.
