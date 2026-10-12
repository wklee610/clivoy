# Execution and security

Clivoy is intended for trusted callers of a self-hosted gateway. It accepts
multiple server API keys with identical permissions; it does not provide per-user
authorization, quotas, or tenant isolation. Every holder of an accepted key can
call every configured model and select native model IDs through configured CLI
and Ollama providers. Configured aliases and the model catalog are
not model access allowlists; the provider's permissions and usage limits apply.

`clivoy init` generates a local token with the prefix `sk-clivoy-` followed by
64 hex characters encoding 32 random bytes, and stores it in the `keys` array of
`auth.json`, referenced by `"auth_file": "auth.json"` in the configuration.
`clivoy keygen` prints a new key in the same format without saving it; add its
output to that array manually to allow it. Existing keys without the prefix
remain valid and are not replaced automatically. Default state lives under
`~/.clivoy` on macOS/Linux or `%USERPROFILE%\.clivoy` on Windows: `clivoy.json`,
`auth.json` and `logs/clivoy.log`. An absolute `CLIVOY_HOME` changes
the root. Managed Unix directories use `0700` and files use `0600`; Windows
access is governed by the user's directory ACLs.
Existing state roots and log directories must not be
symlinks or non-directories. On Unix, group/other permissions cause an error
with `chmod 700` guidance rather than being silently changed. `CLIVOY_HOME`
must be absolute and nonempty when set; leave it unset for the default.
`auth.json` and legacy `*.key` files are excluded by `.gitignore` and must not be
shared publicly. Protect any custom credential filename as well. On Unix,
`init` creates `auth.json` with mode `0600`. Startup and diagnostics do not print
keys; `clivoy auth` explicitly prints only the first active key.
Add or remove entries in `keys`, then restart the server to apply changes.
A nonempty configured key environment variable, by default `CLIVOY_API_KEY`,
overrides the entire file: only the environment key is accepted. Leave it unset
when starting or restarting the server to enable all file keys.

Legacy `api_key_file` configurations still read a plain-text key file such as
`clivoy.json.key`. New initialization creates `auth.json`; it does not migrate
or replace existing credentials automatically.

Bare `start` and `serve` initialize a missing default configuration before starting.
`doctor` and `auth` only read it; explicit `--config` never initializes
files. `init --output` creates `auth.json` beside the chosen configuration and
refuses to overwrite either file. Existing working-directory configurations
are not moved automatically and remain usable
with `--config`. Relative `auth_file` paths are resolved from the chosen
configuration's directory. Package installation does not initialize per-user
state; initialize it as the user who will run the gateway, not through root package hooks.

`start` runs a detached server as the invoking user and confirms readiness before
reporting success. One `CLIVOY_HOME` manages one daemon. Its private `daemon.json`
contains the local control token along with process/configuration metadata;
`daemon.lock` and `daemon-command.lock` coordinate the managed process and control
commands. On Unix these files use `0600` within the private state directory;
Windows relies on the user's directory ACLs. Do not publish the daemon state file.

`stop` uses a separate authenticated loopback HTTP control channel to request
graceful shutdown rather than killing a process based only on a stored PID.
It waits for shutdown and reports an error on timeout. `status` and `stop` act
on the current state root. Restart reuses recorded configuration and listen
overrides for omitted options. CLI credentials still belong to the same user;
detaching the process does not create an isolation boundary. These commands
do not install a system service or enable automatic startup after reboot.

New configurations bind to `0.0.0.0:9610` on all IPv4 interfaces. Local clients
use `127.0.0.1`; remote clients use the server's reachable IP and the same gateway
key. Existing configurations keep their saved listen address. Clivoy itself serves
HTTP; use a TLS reverse proxy or a private network for remote access. `/healthz`
is intentionally public and reports only process liveness. The daemon's separate
authenticated control channel remains on loopback.

API requests cannot choose command paths, command-line flags, filesystem paths,
or model-server URLs. Provider routing is loaded from an administrator-owned JSON
file. Conversation requests may select `<provider>/<native-model-id>` for a
configured CLI provider; Clivoy validates the ID and passes it as a separate
`--model` argument. Ollama providers also accept native model IDs, passed through
the configured server's HTTP API. Exact configuration aliases take precedence.
Generic `openai` providers and image generation accept configured aliases only.
The administrator can supply `command` as an executable-and-arguments array;
its entries are passed literally without shell parsing. Legacy executable-only
strings are still supported. When `command` is omitted, `[]`, `null` or the legacy
`""`, Clivoy discovers installed CLIs through `PATH` and
known installation locations, including supported app bundles and npm entries.
Keep those locations under trusted control. An explicit `command` is authoritative
and never falls back to another installation on failure.
New configuration initialization saves detected absolute paths as one-element
arrays in each provider's `command`; subsequent runs use those saved paths. Existing configurations are not
rewritten automatically, and `doctor` and `models` do not save discovery results.
Clear `command` to resume automatic discovery if its executable moves. A missing
CLI warning during initialization does not prevent configuration/authentication file
creation; set the first entry of the relevant provider's `command` to the correct
absolute executable path if needed.
CLI processes are launched without an intermediate shell. Codex, Claude and
Gemini receive prompts on stdin; Grok reads a private temporary prompt file.
Prompt content is never passed as a command-line argument.
Each request uses a private directory in the operating system's temporary
directory unless a working directory was explicitly configured. The request
directory is removed after execution. CLI subprocesses exclude Clivoy's server
key and unrelated configured upstream key environment variables. Gemini retains
its native `GEMINI_API_KEY` and `GOOGLE_API_KEY`, and Grok retains `XAI_API_KEY`,
even when those variables also authenticate their HTTP provider. The gateway key is always
excluded, even if configured with a provider's native variable name.
Native CLI credentials and caches remain in their own locations; the Clivoy
state root does not relocate them. Grok accesses its native login file through
`GROK_AUTH_PATH` as described below.

`clivoy models` starts configured CLIs to query their metadata interfaces without
a generation prompt; `--refresh` has the same behavior. Only the configured
executable is used, without the additional generation arguments. Metadata subprocesses
use temporary working directories, bounded execution time and the credential
environment filtering described above. Claude uses bare initialization;
Gemini and Grok also apply their isolated integration settings described below.
Gemini creates an ACP session to read its model metadata, without submitting a
prompt; native authentication, cache and session writes may still occur. Grok
requests metadata without creating a session, but may refresh its native login.
Failed queries do not fall back to Clivoy's own
cached models or fixed selectors; configured aliases remain in the output.
Configured Ollama providers receive bounded HTTP model-list requests during
server startup and every `clivoy models` listing.
The default address is loopback; a custom `base_url` contacts that configured
server with its upstream key, if configured. Discovery does not download or run
models. CLIs may return their own cached or bundled catalogs; entries do not
prove account access, and callers may request native model IDs absent from the
catalog. Server startup still uses local CLI metadata/selectors and an Ollama
query; the inspection command does not reload that server catalog or save its
results to Clivoy's configuration.

Gemini and Grok CLI adapters currently accept text only.
Image input is bounded by body size, image count and decoded inline byte limits.
Codex image attachments use private temporary files removed after execution;
Claude receives base64 images on stdin. Remote image URLs are only passed to HTTP
backends and are never fetched by Clivoy. Configure the upstream's network access
appropriately for its callers: that upstream may retrieve caller-supplied URLs.
Clivoy also returns generated image URLs without downloading them.

By default, Codex is invoked with read-only sandboxing, no interactive approvals, ephemeral
sessions, web search disabled, and user configuration/rules ignored. Claude uses
no tools, strict empty MCP configuration, no session persistence, no slash commands,
no browser integration, and hooks disabled where CLI settings allow it.
Enterprise-managed hooks can override user hook settings.

The administrator can explicitly opt into less restricted execution with
`"command": ["codex", "--yolo"]` (or
`--dangerously-bypass-approvals-and-sandbox`) and
`"command": ["claude", "--dangerously-skip-permissions"]`. For Codex, Clivoy
omits its read-only sandbox and fixed approval setting when either option is
present. For Claude, it omits the empty tool list and fixed `dontAsk` permission
mode. The CLI can then use its tools without approval prompts and act with the
server user's filesystem and process permissions. The remaining output,
configuration and session controls still apply. These options are never added
by initialization, and API callers cannot set them. Native tool activity remains
inside the CLI and is not exposed as API function calls.

Gemini receives a temporary system-settings file that overrides its user and
workspace tool, hook, MCP, skill, IDE and context integrations. Extensions are
disabled, the context filename is unique to the request, and directory-tree and
extra-directory memory loading are disabled. Caller `@` references are encoded
inside the JSON conversation to avoid CLI file expansion. Browser login prompts
and system-prompt file overrides are disabled. Authentication and model settings
still come from the native CLI environment and user settings. Gemini's own caches
and session logs remain in its home directory under its retention settings;
Clivoy removes its temporary working directory and settings; the CLI's own
history is retained according to its settings.

Grok Build runs with a private temporary `GROK_HOME` and request working directory.
It rejects `work_dir` configuration to avoid importing project configuration.
Personal configuration, hooks, MCP servers, plugins and session history are not
imported. `GROK_AUTH_PATH` points to the original native login file; Grok owns its
normal locking and token refresh writes. Clivoy does not read or copy credential
contents. An existing `GROK_AUTH_PATH` is respected, otherwise it uses `auth.json`
under the original `GROK_HOME` or `~/.grok`. Its sandbox profile extends Grok's
read-only policy by making the login file's parent directory writable for native
locks and atomic refresh. API-key authentication uses `XAI_API_KEY`. Custom
authentication commands and credentials kept only in personal configuration are
not supported. A detected `managed_config.toml` or `requirements.toml` in the
original Grok home or `/etc/grok` causes an error, preserving the managed policy
boundary; those installations can connect through the HTTP provider.

The Grok adapter disables its effective tool set and requests noninteractive
permissions, with subagents, memory, web and plan behavior disabled. Caller `@`
references are encoded inside the JSON conversation to prevent file expansion.
Temporary prompt, configuration and working directories are removed after
execution; the native login file remains available for subsequent requests.

CLI behavior and OS sandbox support depend on the installed CLI version.
These controls do not isolate a process from all files accessible to its
operating-system account.

Do not give untrusted callers access to a CLI running under a sensitive account.
Use a dedicated account or container/VM with restricted mounts when stronger
isolation is required. Treat a configured `work_dir` as an explicit grant of
context to the CLI. Read-only execution is not a confidentiality boundary.

Timeouts and disconnections cancel work. Unix cancellation targets the process
group; Windows attempts process-tree termination. The server bounds request
bodies, queue length, concurrent execution, provider output, and stream size.
It does not log request content or return raw CLI stderr/upstream error bodies.
Runtime logs go to the state root's `logs/clivoy.log`. Foreground `serve` also
logs to stderr; detached `start` discards standard streams. A custom `log_file`
is configuration-relative unless absolute. `"-"` disables file logging, leaving
no background runtime log; foreground stderr remains available. Startup errors
are reported to the initiating command through a separate handshake. Keep custom
log locations private too. HTTP-only containers running foreground `serve`
can use an explicit mounted configuration and `"log_file": "-"`
without initializing a home-directory state tree.
The runtime log rotates at 10 MiB and retains three backups. Multiple Clivoy
processes must use different `log_file` paths and listen addresses; distinct
managed daemons also need different state roots.

Provider CLI authentication, service permissions, and usage limits remain the
responsibility of the installed provider. Clivoy's license covers Clivoy itself.

## Reporting

Use the repository's private vulnerability reporting feature if enabled. Otherwise
open a minimal issue asking for a private reporting channel; do not publish an
exploit containing credentials or private data. No private response address or
security response SLA has been established yet.
