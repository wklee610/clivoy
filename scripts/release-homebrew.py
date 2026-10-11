#!/usr/bin/env python3
"""Generate a binary Homebrew formula from verified GoReleaser artifacts."""

import argparse
import hashlib
import re
from pathlib import Path


def generate(tag: str, dist: Path, repository: str, *, local: bool = False) -> str:
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", tag):
        raise ValueError("tag must be a v-prefixed semantic version")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("repository must be owner/name")
    version = tag[1:]
    # as_uri escapes spaces, quotes and Ruby interpolation characters in local
    # paths. Archive names below contain only the validated version/platform.
    archive_url = (dist.resolve().as_uri() if local else
                   f"https://github.com/{repository}/releases/download/{tag}")
    checksums = {}
    for line in (dist / "checksums.txt").read_text().splitlines():
        digest, filename = line.split(maxsplit=1)
        if not re.fullmatch(r"[a-f0-9]{64}", digest):
            raise ValueError("invalid SHA-256 checksum")
        checksums[filename.lstrip("*")] = digest

    lines = [
        "# Generated from release artifacts; do not edit checksums by hand.",
        "class Clivoy < Formula",
        '  desc "Local coding CLI and model server API gateway"',
        f'  homepage "https://github.com/{repository}"',
        f'  version "{version}"',
        '  license "Apache-2.0"',
        "",
    ]
    for platform, goos in [("macos", "darwin"), ("linux", "linux")]:
        lines.append(f"  on_{platform} do")
        for arch, goarch in [("arm", "arm64"), ("intel", "amd64")]:
            filename = f"clivoy_{version}_{goos}_{goarch}.tar.gz"
            expected = checksums[filename]
            actual = hashlib.sha256((dist / filename).read_bytes()).hexdigest()
            if actual != expected:
                raise ValueError(f"checksum mismatch: {filename}")
            lines.extend([
                f"    on_{arch} do",
                f'      url "{archive_url}/{filename}"',
                f'      sha256 "{expected}"',
                "    end",
            ])
        lines.extend(["  end", ""])
    lines.extend([
        "  def install",
        '    bin.install "clivoy"',
        "  end",
        "",
        "  test do",
        '    assert_match version.to_s, shell_output("#{bin}/clivoy version")',
        "  end",
        "end",
        "",
    ])
    return "\n".join(lines)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--dist", type=Path, default=Path("dist"))
    parser.add_argument("--repository", default="wklee610/clivoy")
    parser.add_argument("--local", action="store_true",
                        help="use verified local file:// archives instead of GitHub release URLs")
    parser.add_argument("--output", type=Path, default=Path("dist/clivoy.rb"))
    args = parser.parse_args()
    formula = generate(args.tag, args.dist, args.repository, local=args.local)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(formula)


if __name__ == "__main__":
    main()
