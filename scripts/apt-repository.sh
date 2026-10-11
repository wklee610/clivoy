#!/usr/bin/env bash
# Build a latest-stable-only APT repository. Run on Debian/Ubuntu with apt-utils.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 DEB_DIRECTORY OUTPUT_DIRECTORY" >&2
  exit 2
fi
: "${APT_GPG_FINGERPRINT:?Set the complete signing-key fingerprint}"
if [[ ! "$APT_GPG_FINGERPRINT" =~ ^[[:xdigit:]]{40}$ ]]; then
  echo 'APT_GPG_FINGERPRINT must be a full 40-character fingerprint' >&2
  exit 2
fi

deb_directory=$(cd "$1" && pwd)
mkdir -p "$2"
output_directory=$(cd "$2" && pwd)
if [[ -n "$(ls -A "$output_directory")" ]]; then
  echo 'Output directory must be empty; old metadata must not be signed' >&2
  exit 2
fi
for command in apt-ftparchive dpkg-deb gpg gzip; do
  command -v "$command" >/dev/null
done

mkdir -p "$output_directory/pool/main/c/clivoy"
shopt -s nullglob
packages=("$deb_directory"/*.deb)
if [[ ${#packages[@]} -ne 2 ]]; then
  echo 'Expected exactly one amd64 and one arm64 DEB package' >&2
  exit 2
fi
architectures=' '
package_version=''
for package in "${packages[@]}"; do
  name=$(dpkg-deb --field "$package" Package)
  arch=$(dpkg-deb --field "$package" Architecture)
  version=$(dpkg-deb --field "$package" Version)
  if [[ "$name" != clivoy || ! "$arch" =~ ^(amd64|arm64)$ || "$architectures" == *" $arch "* ]]; then
    echo "Unexpected package: $package" >&2
    exit 2
  fi
  if [[ -n "$package_version" && "$package_version" != "$version" ]]; then
    echo 'Both DEB packages must have the same version' >&2
    exit 2
  fi
  package_version=$version
  architectures+="$arch "
  cp "$package" "$output_directory/pool/main/c/clivoy/"
done

cd "$output_directory"
for arch in amd64 arm64; do
  mkdir -p "dists/stable/main/binary-$arch"
  apt-ftparchive --arch "$arch" packages pool > "dists/stable/main/binary-$arch/Packages"
  gzip -n -9 -c "dists/stable/main/binary-$arch/Packages" > "dists/stable/main/binary-$arch/Packages.gz"
done
apt-ftparchive \
  -o APT::FTPArchive::Release::Origin=Clivoy \
  -o APT::FTPArchive::Release::Label=Clivoy \
  -o APT::FTPArchive::Release::Suite=stable \
  -o APT::FTPArchive::Release::Codename=stable \
  -o APT::FTPArchive::Release::Architectures='amd64 arm64' \
  -o APT::FTPArchive::Release::Components=main \
  release dists/stable > dists/stable/Release

sign() {
  printf '%s' "${APT_GPG_PASSPHRASE:-}" | gpg --batch --yes --pinentry-mode loopback \
    --passphrase-fd 0 --local-user "$APT_GPG_FINGERPRINT" --digest-algo SHA256 "$@"
}
sign --clearsign --output dists/stable/InRelease dists/stable/Release
sign --armor --detach-sign --output dists/stable/Release.gpg dists/stable/Release
gpg --batch --export "$APT_GPG_FINGERPRINT" > clivoy-archive-keyring.gpg
gpg --batch --armor --export "$APT_GPG_FINGERPRINT" > clivoy-archive-keyring.asc
printf '%s\n' "$APT_GPG_FINGERPRINT" > signing-key-fingerprint.txt
gpgv --keyring "$output_directory/clivoy-archive-keyring.gpg" dists/stable/InRelease
