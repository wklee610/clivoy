# Distribution and releases

For a local build and first start, use the [installation guide](installation.md).
This reference covers GitHub Releases, Homebrew, APT and GHCR, including
maintainer setup. You can also build a local checkout with
`go build -o clivoy ./cmd/clivoy` (use `clivoy.exe` on Windows).

## Supported packages

| Channel | Platforms | Installation |
| --- | --- | --- |
| GitHub Releases | Windows, macOS, Linux; amd64 and arm64 | Download a ZIP (Windows) or tar.gz and verify `checksums.txt` |
| Homebrew tap | macOS and Linux; amd64 and arm64 | `brew install wklee610/clivoy/clivoy` |
| APT | Debian/Ubuntu; amd64 and arm64 | Add the signed repository, then `sudo apt-get install clivoy` |
| GitHub Container Registry | Linux; amd64 and arm64 | `docker pull ghcr.io/wklee610/clivoy:latest` |
| Go modules | Windows, macOS, Linux | `go install github.com/wklee610/clivoy/cmd/clivoy@latest` |

GitHub Packages hosts the container in GHCR. GitHub Releases hosts native
archives and DEBs. `go install` uses the public Git repository/Go module proxy;
GitHub Packages does not provide a separate Go package registry.

The minimum Go version for a source build is 1.25.0. Binary users do not need
Go. The gateway does not install Codex CLI, Claude Code, Gemini CLI, Grok Build,
Ollama, LiteLLM or vLLM; configure the providers you need separately. A successful cross-build
does not substitute for native testing: CI runs tests on Linux, macOS, and
Windows hosted runners and cross-builds all six OS/architecture combinations.

## User configuration after installation

Package managers install the executable, without starting a system service or
creating a root user's configuration. As your normal user:

```sh
clivoy start
clivoy status
```

The first start initializes missing default files under `~/.clivoy` on macOS/Linux
or `%USERPROFILE%\.clivoy` on Windows. Existing configuration and keys are kept.
Run `clivoy auth` to retrieve your client key. These commands do not enable startup
after reboot. See the [usage guide](usage.md) for state paths, custom configurations,
key rotation, logs and background process management.

## Homebrew

```sh
brew tap wklee610/clivoy
brew install wklee610/clivoy/clivoy
clivoy version
```

The tap is the separate public `wklee610/homebrew-clivoy` repository. Every
release generates `clivoy.rb` from the four macOS/Linux archives, verifies
their SHA-256 against the release manifest, and commits that formula to the
tap. The formula is also attached to the GitHub release. No placeholder
checksums are committed to this repository, and no Homebrew Core submission
is required for the tap installation.

Before publishing, use the [local Homebrew validation procedure](#local-homebrew-installation)
to generate a formula with `file://` archive URLs and register a private local tap.

## APT

Install the download prerequisites from your distribution's existing repositories,
then download the Clivoy public signing key:

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl gnupg
curl -fsSLo /tmp/clivoy-archive-keyring.gpg \
  https://wklee610.github.io/clivoy/apt/clivoy-archive-keyring.gpg
gpg --show-keys --with-fingerprint /tmp/clivoy-archive-keyring.gpg
```

The signing-key fingerprint is **`2731106AE613E8E58552C2D26C9D05834AB8E2D9`**,
also included in the release announcement. The next block checks that fingerprint
and stops before installation if it does not match, then adds the signed repository:

```sh
(
  set -eu
  CLIVOY_APT_FINGERPRINT="$(gpg --batch --show-keys --with-colons /tmp/clivoy-archive-keyring.gpg \
    | awk -F: '$1 == "pub" { primary=1 } $1 == "fpr" && primary { print $10; primary=0 }')"
  test "$CLIVOY_APT_FINGERPRINT" = "2731106AE613E8E58552C2D26C9D05834AB8E2D9"
  sudo install -d -m 0755 /etc/apt/keyrings
  sudo install -m 0644 /tmp/clivoy-archive-keyring.gpg /etc/apt/keyrings/clivoy.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/clivoy.gpg] https://wklee610.github.io/clivoy/apt stable main" \
    | sudo tee /etc/apt/sources.list.d/clivoy.list >/dev/null
  sudo apt-get update
  sudo apt-get install clivoy
)
```

APT verifies the signed `InRelease` and the index/package hashes. The key is
scoped to this repository through `signed-by`; installation does not use
`apt-key`. Packages install `/usr/bin/clivoy` without starting a background
service, creating a root-owned user state directory, or changing provider credentials.

The repository contains the most recently published stable version for each
architecture. Older DEBs remain in GitHub Releases. Publishing replaces the
APT site through GitHub Pages. There is no `Valid-Until` expiration
in the metadata, so infrequent project releases do not expire installations;
key rotation and metadata freshness remain maintainer responsibilities.

For an individual downloaded DEB, verify the SHA-256 in `checksums.txt`, then
use `sudo apt-get install ./clivoy_<version>_amd64.deb` (or the arm64 asset).

## Containers and GitHub Packages

The image runs as UID/GID 65532 and contains Clivoy plus CA certificates. It
has no shell and does not bundle coding CLIs or their login state. It is
intended for HTTP providers such as vLLM, Ollama, LiteLLM, or a remote API.
Use a host binary for an existing local CLI login, or build a separate image
with the required CLI and deliberately provision its credentials.

Prepare a separate readable HTTP-backend configuration, such as
`clivoy.container.json`, and mount it at `/etc/clivoy/config.json`. Set its
`listen` to `0.0.0.0:9610`, `api_key_env` to `CLIVOY_API_KEY`, and **`log_file`
to `"-"`**. File logging is unnecessary with container stderr collection, and
this setting avoids writing to a potentially read-only home directory. Configure
HTTP providers only for this image. Keep the API token in the environment.
After initializing Clivoy on the host, retrieve its default token:

```sh
export CLIVOY_API_KEY="$(clivoy auth)"
docker run --rm -p 127.0.0.1:9610:9610 \
  -e CLIVOY_API_KEY \
  -v "$PWD/clivoy.container.json:/etc/clivoy/config.json:ro" \
  ghcr.io/wklee610/clivoy:latest
```

Use the environment variable name configured by your `api_key_env` setting.
The image starts with an explicit `--config /etc/clivoy/config.json`, so it does
not automatically create configuration or keys. The mounted file must already
exist. This foreground `serve` process remains attached to the container's
stderr collection. With HTTP providers and file logging disabled, user state
initialization is not needed in the container; do not replace it with `start`.
The environment value overrides all file credentials, accepting only that key,
so this example does not require mounting the host's private `auth.json` into the container.
The process cannot read a host file restricted to a different UID; grant the
container user read access to the mounted configuration. `localhost` inside
the container refers to the container. Use a reachable service hostname for
upstreams; Docker Desktop supports `host.docker.internal`, while Linux Docker
may need `--add-host=host.docker.internal:host-gateway`.

Prefer a release tag such as `ghcr.io/wklee610/clivoy:v1.0.0`, or an image
digest, when reproducibility matters. `latest` is updated by each stable
release. GHCR packages must be public to allow anonymous pulls.

## Maintainer release setup

1. Create the public `wklee610/homebrew-clivoy` repository with an initial
   commit on its default branch. Create a dedicated SSH key without a
   passphrase and add its public key to that tap's **Deploy keys**, enabling
   **Allow write access**. Store the private key as the `HOMEBREW_TAP_SSH_KEY`
   Actions secret in `wklee610/clivoy`. This key must be dedicated to the tap;
   its branch rules must allow the bot update.
2. Create a dedicated OpenPGP signing key with signing capability and retain
   an offline backup/revocation certificate. Export the ASCII-armored private
   key to the `APT_GPG_PRIVATE_KEY` Actions secret. Set the full 40-character
   primary fingerprint as the `APT_GPG_FINGERPRINT` repository variable. If
   encrypted, put the passphrase in `APT_GPG_PASSPHRASE`. The workflow includes
   the public fingerprint in each release announcement for users to compare.
3. Set repository **Settings → Pages → Build and deployment → Source** to
   **GitHub Actions**. Configure the `github-pages` environment's deployment
   rules to permit release tags (`v*`). This workflow owns the whole Pages
   site; do not deploy another site to that same destination independently.
4. Allow Actions to publish packages with `GITHUB_TOKEN`. After the initial
   GHCR publication, set package visibility to **Public** and confirm this
   repository has package access. No personal container-registry token is
   needed for the workflow.
5. Ensure CI is green and the intended commit is on the release branch.
   Protect release tags from untrusted writers: tagged code executes with
   publishing credentials. Then create and push a stable tag such as
   `v1.0.0` when ready to publish.

Only stable `vMAJOR.MINOR.PATCH` tags publish. The release workflow calls the
complete CI workflow before it can publish, checks required configuration,
builds the archives and DEBs, adds the generated formula, and stages a draft
release with all assets before making it public. Downstream jobs update the
tap, sign/deploy APT metadata, and push the multiarch image. Permissions are
scoped to the publishing jobs; normal pull-request CI has read-only access.

Publishing jobs are independent after the native release. If a downstream
job fails, use **Re-run failed jobs** after fixing the configuration rather
than moving the tag. If a run stopped while creating the draft, inspect and
remove the incomplete draft before restarting the binaries job. Never replace
already published assets with different binaries for the same version.

Publish versions in increasing order. The workflow serializes active release
runs, but its tap, APT, and `latest` tags follow the last completed publication.
Backport/prerelease channels need a separate policy before they are enabled.

## Local validation

With GoReleaser v2.14.0 installed:

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish
python3 scripts/release-homebrew.py --tag v0.0.0-dev
ruby -c dist/clivoy.rb
```

On Debian/Ubuntu with `apt-utils`, `gnupg`, and Docker installed, run
`bash scripts/package-smoke.sh dist`. It generates a disposable signing key,
builds and verifies the APT metadata, then performs `apt-get update` and
`apt-get install clivoy` in an isolated Ubuntu container. It does not modify
the host's APT configuration. CI also builds/runs the nonroot Docker image.
These commands do not publish anything.

### Local Homebrew installation

This procedure tests a snapshot through Homebrew without a GitHub release or a
public tap. It requires Go, GoReleaser v2.14.0, Python 3, Git, and Homebrew. Run
from the repository root, and use an unused local tap name; the example uses
`clivoy/local`. Homebrew may still require supported development tools even when
the formula only installs a prebuilt executable.

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish
mkdir -p .cache
CLIVOY_BREW_TEST="$(mktemp -d "$PWD/.cache/homebrew-test.XXXXXX")"
CLIVOY_LOCAL_TAP=clivoy/local
mkdir -p "$CLIVOY_BREW_TEST/artifacts" "$CLIVOY_BREW_TEST/tap/Formula"
cp dist/clivoy_0.0.0-dev_*.tar.gz dist/checksums.txt "$CLIVOY_BREW_TEST/artifacts/"
python3 scripts/release-homebrew.py --tag v0.0.0-dev \
  --dist "$CLIVOY_BREW_TEST/artifacts" --local \
  --output "$CLIVOY_BREW_TEST/tap/Formula/clivoy.rb"
ruby -c "$CLIVOY_BREW_TEST/tap/Formula/clivoy.rb"
git -C "$CLIVOY_BREW_TEST/tap" init
git -C "$CLIVOY_BREW_TEST/tap" add Formula/clivoy.rb
git -C "$CLIVOY_BREW_TEST/tap" \
  -c user.name="Clivoy local test" -c user.email="clivoy-local@example.invalid" \
  commit -m "Add local Clivoy snapshot"
CLIVOY_TAP_URI="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve().as_uri())' "$CLIVOY_BREW_TEST/tap")"
HOMEBREW_NO_AUTO_UPDATE=1 brew tap --custom-remote "$CLIVOY_LOCAL_TAP" "$CLIVOY_TAP_URI"
HOMEBREW_NO_AUTO_UPDATE=1 brew install "$CLIVOY_LOCAL_TAP/clivoy"
```

The snapshot version is `0.0.0-dev` in the checked-in GoReleaser configuration.
`--local` verifies all four macOS/Linux archives against `checksums.txt` before
writing the formula, then uses absolute, escaped `file://` URLs. Copying the
artifacts out of `dist` keeps those URLs valid across later snapshot builds.
Keep that artifact directory while using the local tap. The Git commit above
belongs only to the new local tap; these commands do not push or publish it.

After installation succeeds, verify the installed package:

```sh
clivoy version
brew test "$CLIVOY_LOCAL_TAP/clivoy"
```

`brew test` has its own development-tools checks and can require supported CLT
even when a bottle installed successfully.

This checks local file downloads, SHA-256 verification, and—if installation
succeeds—Homebrew installation and the formula test. It does not verify public
HTTP downloads, GitHub release visibility, or the published tap. Omitting
`--local` keeps the normal GitHub release URLs.

On macOS, a binary-archive formula without a Homebrew `bottle` block still uses
Homebrew's source-install path and can fail its Command Line Tools checks.
A genuine native Homebrew bottle is a separate package format; the GoReleaser
archive alone is not a bottle. Use supported developer tools or validate a
proper native bottle through the local tap; do not disable Homebrew's checks.

Verify runtime behavior separately with
`python3 scripts/daemon_smoke.py "$(command -v clivoy)"`. This uses temporary
configuration directories and sends no model requests.

Keep the local tap, artifact directory, and any native bottles for reinstalls.
To reinstall from the existing files:

```sh
HOMEBREW_NO_AUTO_UPDATE=1 brew reinstall clivoy/local/clivoy
```

The configuration follows the official [GoReleaser archives](https://goreleaser.com/customization/package/archives/)
and [nFPM](https://goreleaser.com/customization/package/nfpm/) documentation,
[Homebrew taps](https://docs.brew.sh/Taps), Debian's
[APT authentication](https://manpages.debian.org/bookworm/apt/apt-secure.8.en.html),
and GitHub's [Pages workflows](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages)
and [container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
documentation.
