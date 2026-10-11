# Contributing

Use Go 1.25 or later. Start with an issue describing the interoperability problem
or a small pull request with a reproducible case. Changes to API behavior should
include a test for the affected protocol and update `docs/compatibility.md`.

The server currently uses the Go standard library only. Keep provider-specific
behavior in `internal/provider`, wire format conversion in `internal/server`,
and shared request/result types in `internal/engine`.

Before submitting:

```sh
gofmt -w cmd internal
go test -race ./...
go vet ./...
```

For protocol changes, build the binary and run `scripts/sdk_smoke.py` with the
versions in `scripts/requirements-sdk.txt`. Use `go test ./...` on Windows if the
race toolchain is unavailable. Tests must be deterministic and must not require
cloud credentials, installed AI CLIs, or paid inference.

Never include tokens, private prompts, CLI login files, or raw provider diagnostic
output in fixtures or bug reports. Use artificial examples. See `SECURITY.md` for
reporting vulnerabilities and `docs/distribution.md` for release procedures.

Contributions are made under the project's Apache-2.0 license.
