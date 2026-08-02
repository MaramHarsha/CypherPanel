#!/bin/sh
# Rebuild a draft release from source, prove it matches, sign it, publish it.
#
# Run this on the machine holding the offline key. Never in CI.
#
# The check that matters is the rebuild. Downloading the binaries and the
# SHA256SUMS from the same draft and confirming they agree proves nothing: an
# attacker who can write to the draft writes both, and they agree perfectly.
# That circularity would have had the release manager's offline key turn
# CI-supplied bytes into an authentic fleet release — the compromise the offline
# key exists to survive, laundered through the one step trusted to catch it.
#
# So the artifacts are rebuilt here, from the tag, with CI's exact flags, and
# compared byte for byte. -trimpath and CGO_ENABLED=0 make Go builds
# reproducible, which is what lets a mismatch carry meaning. The signature then
# attests "I built this from source I read", not "CI agrees with itself".
set -eu

VERSION="${1:?usage: release-sign.sh vX.Y.Z}"
WORK="${WORK:-dist/sign}"
GO_VERSION_WANT="${GO_VERSION_WANT:-go1.25}"

: "${CYPHER_RELEASE_SIGNING_KEY:?not set — see docs/dev/release-signing.md}"
command -v gh >/dev/null || { echo "gh is required"; exit 1; }

# A different toolchain produces different bytes, which would look exactly like
# a compromised artifact. Fail on the ambiguity rather than teaching the
# operator that mismatches are normal.
have="$(go env GOVERSION)"
case "$have" in
  "$GO_VERSION_WANT"*) ;;
  *) echo "go toolchain is $have, release built with $GO_VERSION_WANT*."
     echo "Install the matching toolchain — a version skew is indistinguishable"
     echo "from a tampered artifact once the comparison fails."
     exit 1 ;;
esac

rm -rf "$WORK"
mkdir -p "$WORK/built" "$WORK/draft"

echo "==> Checking out $VERSION from the repository, not from the release"
git fetch --tags --force >/dev/null 2>&1 || true
git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null \
  || { echo "tag $VERSION not found locally"; exit 1; }
# A worktree, so an unclean working tree cannot leak into what gets signed.
git worktree add --detach --force "$WORK/src" "$VERSION" >/dev/null

echo "==> Rebuilding with CI's flags"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -C "$WORK/src/core" -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$(pwd)/$WORK/built/cypherd-linux-$arch" ./cmd/cypherd
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -C "$WORK/src/agent" -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$(pwd)/$WORK/built/cypher-agent-linux-$arch" ./cmd/cypher-agent
done

echo "==> Downloading the draft's artifacts"
gh release download "$VERSION" -D "$WORK/draft" -p 'cypher*' -p 'SHA256SUMS'

echo "==> Comparing the draft against the rebuild"
mismatch=0
for f in "$WORK"/built/*; do
  name="$(basename "$f")"
  if [ ! -f "$WORK/draft/$name" ]; then
    echo "  MISSING from the draft: $name"; mismatch=1; continue
  fi
  if cmp -s "$f" "$WORK/draft/$name"; then
    echo "  ok  $name"
  else
    echo "  DIFFERS: $name"; mismatch=1
  fi
done
# Anything the draft carries that we did not build is unaccounted for, which is
# the shape an injected artifact takes.
for f in "$WORK"/draft/cypher*; do
  name="$(basename "$f")"
  [ -f "$WORK/built/$name" ] || { echo "  UNEXPECTED in the draft: $name"; mismatch=1; }
done
if [ "$mismatch" -ne 0 ]; then
  echo
  echo "REFUSING TO SIGN. The published artifacts are not what this tag builds."
  echo "Do not publish. Investigate the release pipeline before anything else."
  exit 1
fi

echo "==> Signing our own manifest"
# Ours, computed from the rebuild — not the file the draft supplied. If they
# agree it makes no difference, and if they ever disagree this is the one that
# should carry a signature.
( cd "$WORK/built" && sha256sum ./* > SHA256SUMS )
if ! diff -q "$WORK/built/SHA256SUMS" "$WORK/draft/SHA256SUMS" >/dev/null 2>&1; then
  echo "  note: the draft's SHA256SUMS differs in form; signing the rebuilt one"
fi
go run -C core ./cmd/release-sign \
  -in "$(pwd)/$WORK/built/SHA256SUMS" -out "$(pwd)/$WORK/built/SHA256SUMS.sig"
go run -C core ./cmd/release-sign -verify \
  -in "$(pwd)/$WORK/built/SHA256SUMS" -sig "$(pwd)/$WORK/built/SHA256SUMS.sig" \
  -key "$(go run -C core ./cmd/release-sign -public)"

echo "==> Publishing"
gh release upload "$VERSION" \
  "$WORK/built/SHA256SUMS" "$WORK/built/SHA256SUMS.sig" --clobber
gh release edit "$VERSION" --draft=false

git worktree remove --force "$WORK/src" >/dev/null 2>&1 || true
echo "$VERSION verified against source, signed, and published."
