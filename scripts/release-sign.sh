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

: "${CYPHER_RELEASE_SIGNING_KEY:?not set — see docs/dev/release-signing.md}"
command -v gh >/dev/null || { echo "gh is required"; exit 1; }
command -v go >/dev/null || { echo "go is required (any 1.21+; the exact release toolchain is fetched below)"; exit 1; }

# The EXACT toolchain CI built with, read from the same file CI reads it from
# (go.work, via setup-go's go-version-file). This used to accept any "go1.25*",
# which is not a check: a patch release of Go can change the compiler's output,
# so a signer on 1.25.14 rebuilding what CI made on 1.25.12 would see every
# binary DIFFER — and the script's own words would then tell them not to
# publish. GOTOOLCHAIN makes the go command fetch and use that exact version
# regardless of what is installed, so the comparison below means what it says.
GO_VERSION_WANT="go$(awk '/^go /{print $2; exit}' go.work)"
export GOTOOLCHAIN="$GO_VERSION_WANT"
have="$(go env GOVERSION)"
if [ "$have" != "$GO_VERSION_WANT" ]; then
  echo "go toolchain is $have, the release is built with $GO_VERSION_WANT (go.work)."
  echo "GOTOOLCHAIN=$GO_VERSION_WANT could not switch to it — is GOTOOLCHAIN forced"
  echo "to 'local', or is the network unreachable? A version skew is"
  echo "indistinguishable from a tampered artifact once the comparison fails."
  exit 1
fi

rm -rf "$WORK"
mkdir -p "$WORK/built" "$WORK/draft"

echo "==> Checking out $VERSION from the repository, not from the release"
git fetch --tags --force >/dev/null 2>&1 || true
git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null \
  || { echo "tag $VERSION not found locally"; exit 1; }
# A worktree, so an unclean working tree cannot leak into what gets signed.
git worktree add --detach --force "$WORK/src" "$VERSION" >/dev/null

# CI's flags, LITERALLY — this block and the one in .github/workflows/release.yml
# have to agree exactly or every comparison below fails and no release can ever
# be signed. They previously did not: CI stamped the plane with `main.commit` and
# `main.buildDate` and this rebuilt with neither, so `cmp` was guaranteed to
# mismatch on the very first release. Worse, CI's build date came from `date -u`
# at run time, which nothing can reproduce — the comparison was impossible by
# construction rather than merely misconfigured. Both now derive every stamp
# from the TAG: the commit, the commit's date, and the release key checked in
# beside this script.
echo "==> Rebuilding with CI's flags"
COMMIT=$(git -C "$WORK/src" rev-parse --short HEAD)
BUILD_DATE=$(git -C "$WORK/src" log -1 --format=%cd --date=format:%Y-%m-%dT%H:%M:%SZ)
PUBKEYS=$(tr -d '[:space:]' < "$WORK/src/release-pubkey.txt" 2>/dev/null || true)
PLANE_STAMPS="-X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE"
AGENT_STAMPS="-X main.version=$VERSION -X github.com/MaramHarsha/cypherpanel/agent/updater.publicKeys=$PUBKEYS"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -C "$WORK/src/core" -trimpath -ldflags "-s -w $PLANE_STAMPS" \
      -o "$(pwd)/$WORK/built/cypherd-linux-$arch" ./cmd/cypherd
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -C "$WORK/src/agent" -trimpath -ldflags "-s -w $AGENT_STAMPS" \
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
