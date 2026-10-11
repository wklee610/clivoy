#!/usr/bin/env bash
# Exercise the actual signed APT index and install the native DEB in an isolated
# Ubuntu container. Never touches the host's package sources or installed files.
set -euo pipefail
dist_directory=$(cd "${1:-dist}" && pwd)
script_directory=$(cd "$(dirname "$0")" && pwd)
test_directory=$(mktemp -d)
export GNUPGHOME="$test_directory/gnupg"
mkdir -m 700 "$GNUPGHOME"
trap 'gpgconf --kill gpg-agent; rm -rf "$test_directory"' EXIT
gpg --batch --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'Clivoy packaging test <packaging@example.invalid>' rsa2048 sign 1d
APT_GPG_FINGERPRINT=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" { print $10; exit }')
export APT_GPG_FINGERPRINT
bash "$script_directory/apt-repository.sh" "$dist_directory" "$test_directory/repository"
chmod -R a+rX "$test_directory/repository"

docker run --rm --volume "$test_directory/repository:/repo:ro" ubuntu:24.04 bash -euc '
  mkdir -p /etc/apt/keyrings
  cp /repo/clivoy-archive-keyring.gpg /etc/apt/keyrings/clivoy.gpg
  printf "deb [signed-by=/etc/apt/keyrings/clivoy.gpg] file:/repo stable main\n" > /etc/apt/sources.list
  apt-get update
  apt-get install -y clivoy
  clivoy version
'
