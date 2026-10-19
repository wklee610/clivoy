# Answers from Markdown documents

Clivoy v1.1.0 can search a local Markdown directory and pass relevant excerpts to
your chosen model before it answers. This is retrieval-augmented generation
(RAG). It runs inside the same Clivoy process and API, using the existing provider
login, gateway keys and request limits. No embedding service, vector database or
additional listening port is required.

## Start with the default directory

A new `clivoy init`, or first `start`/`serve` without an existing configuration,
creates an empty `knowledge/default` directory beside `clivoy.json`. It also adds
the `default` knowledge base and an `infra` model backed by Codex. With the default
state root, the directory is `~/.clivoy/knowledge/default` on macOS/Linux and
`%USERPROFILE%\.clivoy\knowledge\default` on Windows. `init --output` creates the
directory beside that custom configuration instead.

Add UTF-8 `.md` files there, authenticate the Codex CLI, and call the `infra`
model. Document edits are picked up at the next successful refresh, normally
within 30 seconds; configuration changes require `clivoy restart`.

```sh
export CLIVOY_API_KEY="$(clivoy auth)"
curl -sS http://127.0.0.1:9610/v1/chat/completions \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"infra","messages":[{"role":"user","content":"What is the backup procedure?"}]}'
```

An empty directory is valid. If search finds no relevant documents, Clivoy
returns a no-match answer without invoking the model. An invalid root or failed
initial index prevents server startup.

## Add RAG to an existing configuration

Upgrading does not edit existing configurations or add document directories.
Create the directory yourself and merge this fragment into `clivoy.json`, keeping
your existing `providers`, models, credentials and other settings:

```json
{
  "knowledge_bases": {
    "default": {
      "root": "knowledge/default",
      "top_k": 10,
      "max_context_tokens": 6000,
      "refresh_interval": "30s"
    }
  },
  "models": {
    "infra": {
      "provider": "codex",
      "knowledge": "default",
      "capabilities": ["chat"]
    }
  }
}
```

`provider` refers to an existing provider entry. Change it to Claude, Gemini,
Grok, Ollama or an OpenAI-compatible HTTP provider as needed. HTTP and Ollama
aliases also need an upstream `model`, just like ordinary aliases. Multiple model
aliases can share the same knowledge base and index; each alias can use a
different backend. Ordinary models without `knowledge` keep their existing
behavior, including direct `<provider>/<native-model>` requests.

For a standalone configuration, see [examples/knowledge.json](../examples/knowledge.json).
Its `root` is `knowledge`, relative to the location where you save the example;
create that directory before starting. It uses the `CLIVOY_API_KEY` environment
variable instead of an auth file. Set a key with
`export CLIVOY_API_KEY="$(clivoy keygen)"` before running
`clivoy serve --config /path/to/knowledge.json`.

| Knowledge base setting | Default when omitted | Accepted values |
| --- | --- | --- |
| `root` | Required | Existing Markdown directory; relative to the configuration file, absolute, or `~/...` |
| `top_k` | `10` | `1`–`50` retrieved chunks, with at most two from one document |
| `max_context_tokens` | `6000` | `256`–`32768` estimated tokens for added retrieval context |
| `refresh_interval` | `"30s"` | Go duration from `"1s"` through `"24h"` |

`~/` expands to the home of the user running Clivoy. Windows absolute paths must
escape backslashes in JSON, for example `"C:\\Users\\you\\Documents\\Notes"`.
Unknown settings, explicit `null` values and values outside the documented ranges
are rejected. `knowledge` must name a configured knowledge base. RAG aliases have
only `["chat"]` capability; omitting `capabilities` supplies that default.

The context estimate is `ceil(UTF-8 byte length / 3)`. The budget includes the
retrieval instructions and serialized document text, titles, paths, headings and
URLs. It does **not** include the original conversation or generated answer, and
is not the upstream model's exact tokenizer or total context-window limit.
Lower the budget or shorten the conversation if the chosen model has less room.

## Markdown and Obsidian notes

Point `root` at a Markdown directory or an Obsidian vault you intend to expose to
gateway callers. Clivoy reads the files without changing them. It uses keyword
matching with title and heading ranking, so a clear question containing the
document's subject works better than an unrelated paraphrase.

The first H1 is used as the title, falling back to the filename. Headings, code
blocks and tables are retained in retrieved text within the context budget.
An optional HTTP(S) URL on the first line supplies a source link. Otherwise the
source list identifies the document by title and its path relative to `root`.

Only `.md` files are indexed. Hidden directories such as `.obsidian`, hidden
files, symlinks, non-regular files and invalid UTF-8 files are skipped. Images,
PDFs and other attachments are not indexed, and Obsidian plugins, embedded notes
and graph relationships are not executed or expanded.

Each file is limited to 2 MiB; oversized files are skipped. One index is bounded
to 5,000 eligible Markdown paths, 32 MiB of eligible document content, 20,000
chunks, 100,000 directory entries and 64 directory levels. Exceeding an aggregate
limit fails that refresh instead of publishing a partial index. These are
resource limits, not a claim about retrieval quality on a particular collection.

## Search and inspect the index

Both routes use the same gateway authentication as chat. Search returns excerpts
without calling a generation backend:

```sh
curl -sS http://127.0.0.1:9610/v1/search \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"knowledge":"default","query":"backup procedure","limit":5}'

curl -sS http://127.0.0.1:9610/v1/knowledge/status \
  -H "Authorization: Bearer $CLIVOY_API_KEY"
```

`POST /v1/search` accepts `knowledge`, `query` and optional `limit`. Omitting
`knowledge` selects `default`; an unknown name returns 404. `query` must be
nonempty and at most 8,192 UTF-8 bytes at the API boundary. Ranking uses up to
2,048 Unicode characters and 32 query concepts. Omit `limit`, or use `0`, for the
configured `top_k`; a positive limit cannot exceed that value. The response has
`object: "list"`, `knowledge`, and `data` entries containing `id`, `path`, `title`,
`heading`, `url`, `text` and `score`. Paths are relative to the configured root.
Search uses the existing server queue, concurrency and request-timeout limits.
Its raw snippets have a separate character limit; `max_context_tokens` bounds
the assembled generation context, not the JSON search response.

`GET /v1/knowledge/status` returns an `object: "list"` with one `data` entry per
knowledge base: `id`, `ready`, `refresh_failed`, `documents`, `chunks`, `skipped`,
`updated_at` and `refresh_interval`. `ready` means an initial snapshot is
available, even when the directory is empty. `updated_at` records the last
successful refresh. `skipped` counts excluded entries, not failed model calls.

Refresh replaces the index only after a complete successful scan. A later read,
resource-limit or refresh failure sets `refresh_failed` and preserves the last
successful snapshot; it does not interrupt other models. Deleted or changed
documents can remain searchable in that snapshot until refresh succeeds. A
successful retry clears `refresh_failed`. `/healthz` remains a process liveness
check; inspect knowledge status to check document refreshes.

## Answers, sources and execution controls

Use a RAG alias with OpenAI Chat Completions, stateless Responses or Anthropic
Messages, with or without SSE streaming. The request and response formats are
the existing Clivoy formats. RAG currently accepts text messages and returns
text answers; image input, API tools, tool-call history and structured JSON
output are unsupported. Backend-specific restrictions still apply.

Clivoy appends a numbered source list to the answer text using only documents
included in the model context. In streams the list arrives after generated text
and before completion. This is a text footer, not vendor-native citation
annotations or a separate `sources` response field. The list shows which
documents were supplied; it does not prove every statement is supported. Model
usage does not count the server-generated footer.

RAG CLI execution reuses only the provider's configured executable and login.
It ignores the ordinary provider's extra `command` arguments and `work_dir`,
using a fresh temporary working directory. Consequently `--yolo` and
`--dangerously-skip-permissions` on ordinary models do not carry over to RAG
aliases. Normal models retain those options.

For RAG, Codex uses read-only execution and disables shell, app, plugin, browser,
code-mode and related integrations; Claude has an empty tool set and no user or
project settings sources. Gemini and Grok retain their restricted integration
settings. These controls depend on the installed CLI and do not create an OS
isolation boundary. Required options unsupported by a CLI cause an error rather
than a retry with weaker settings.

Every accepted gateway key can query every configured knowledge base and RAG
model; there are no per-document or per-user permissions. Retrieved snippets and
their metadata are sent to the selected CLI or HTTP backend, including Ollama;
the backend's hosting and data policies apply. Use a directory whose contents
are appropriate for all gateway callers. See [security details](../SECURITY.md).
