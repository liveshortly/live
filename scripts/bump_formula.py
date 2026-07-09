#!/usr/bin/env python3
"""Rewrite the Homebrew formula to point at a new release.

Usage: bump_formula.py <formula.rb> <version-without-v> <dist-dir>

The dist dir holds the built `live-vX.Y.Z-<os>-<arch>.gz` archives. Homebrew
checksums the file it downloads, so the hashes here are of the .gz — NOT the
`.sha256` files published beside each asset, which cover the extracted binary.
"""
import hashlib
import pathlib
import re
import sys

PLATFORMS = ["darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"]


def main() -> int:
    if len(sys.argv) != 4:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    formula_path, version, dist = pathlib.Path(sys.argv[1]), sys.argv[2], pathlib.Path(sys.argv[3])

    if version.startswith("v"):
        print(f"error: pass the version without a leading v, got {version!r}", file=sys.stderr)
        return 2

    sums = {}
    for p in PLATFORMS:
        gz = dist / f"live-v{version}-{p}.gz"
        if not gz.is_file():
            print(f"error: missing {gz}", file=sys.stderr)
            return 1
        sums[p] = hashlib.sha256(gz.read_bytes()).hexdigest()

    text = original = formula_path.read_text()
    text = re.sub(r'^(\s*version\s+)"[^"]+"', rf'\g<1>"{version}"', text, count=1, flags=re.M)

    for p, digest in sums.items():
        # Each platform block is a url line naming that platform, then its sha256.
        # The version appears twice in the url: the release tag directory and the
        # asset filename. Both must move, or the download 404s.
        pattern = re.compile(
            r'(url\s+"[^"]*/download/)v[^/"]+(/live-v)[^"]+?(-'
            + re.escape(p)
            + r'\.gz"\s*\n\s*sha256\s+")[0-9a-f]{64}(")'
        )
        text, n = pattern.subn(rf"\g<1>v{version}\g<2>{version}\g<3>{digest}\g<4>", text)
        if n != 1:
            print(f"error: expected 1 url+sha256 block for {p}, matched {n}", file=sys.stderr)
            return 1

    if text == original:
        print("formula already up to date")
        return 0

    formula_path.write_text(text)
    print(f"bumped {formula_path} to {version}")
    for p, digest in sums.items():
        print(f"  {p}: {digest}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
