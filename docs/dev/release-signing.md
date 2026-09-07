# Release signing

Every release publishes `SHA256SUMS` and `SHA256SUMS.sig`. The sums prove a
download arrived intact; the signature proves it is ours. Only the second one
survives a compromised release page, because whoever can replace a binary can
replace the checksum file sitting next to it.

This matters more here than for most projects. ADR-010 makes agent binaries
fetched from GitHub Releases and swapped in across a whole fleet, so the release
page is a path to remote code execution on every server an operator owns. ADR-010
§3 is explicit: *artifacts are signed with an offline release key; the agent
verifies against a public key baked into its binary before swapping*.

## The key never reaches CI

The private key is generated on a machine that is not CI, and CI never sees it.
An Actions secret is an online key: it is readable by every workflow in the
repository and by whatever code a compromised tag makes the runner execute, so a
release key stored there would let anyone who reaches CI sign binaries that a
whole fleet installs. That is the exact power this design exists to deny, and
storing the key in Actions would have handed it over while looking like
compliance.

So the split is: **CI builds and publishes a draft; a human signs and
publishes.** A draft release is not public, which makes "unsigned" and
"unreleased" the same state rather than a rule someone has to remember.

Ed25519, raw, over the manifest bytes. `crypto/ed25519` is standard library, so
the agent verifies with no dependency to compromise and no verification service
that has to be reachable at the moment a fleet needs patching.

## Generating the key

Once, on a trusted machine, offline:

```sh
head -c 32 /dev/urandom | base64 -w0 > cypher-release.key
```

Store `cypher-release.key` somewhere durable and offline — a password manager or
a hardware-backed store. Losing it means every future release needs a new key and
a matching agent rollout. Leaking it means an attacker can sign binaries that
every agent in every fleet will accept.

Print the matching public key:

```sh
CYPHER_RELEASE_SIGNING_KEY="$(cat cypher-release.key)" \
  go run -C core ./cmd/release-sign -public
```

Then:

1. **Do not** put the private key in GitHub Actions secrets, or anywhere else CI
   can read. It lives on the signing machine and is exported into the
   environment only for the length of a signing run.
2. Put the public key in **`release-pubkey.txt`** at the repository root and
   commit it. Both CI and `release-sign.sh` read it from the tag, so the
   rebuild reproduces exactly what CI published — a key held in CI settings
   could not be reproduced from a checkout, and the byte-for-byte comparison
   this whole procedure rests on would be impossible. See
   `release-pubkey.README.md`.

   For a one-off local build, the same value goes in by hand:

```sh
go build -ldflags "-X github.com/MaramHarsha/cypherpanel/agent/updater.publicKeys=$RELEASE_PUBKEY" ./cmd/cypher-agent
```

   The variable takes one or two comma-separated base64 keys. Two is what
   rotation needs and the ceiling the parser enforces: release N is signed with
   A and bakes in `A,B`; N+1 is signed with B; N+2 bakes in `B` alone. A list
   that only grows is a trust surface that only grows.

```
RELEASE_PUBKEY = <not yet generated — see "Status" below>
```

## Rehearsing, before the first tag

`make release-rehearsal` runs the release on this machine, end to end, against
throwaway containers it removes afterwards. It builds the artifacts with the
workflow's own ldflags, **rebuilds one of them and compares byte for byte**
(without that, `make release-sign` could never verify a rebuild and the offline
key would be unusable), installs onto an empty database, enrols a server,
restarts the plane, migrates a database made by the PREVIOUS release's binary,
and takes a snapshot to a real S3 endpoint and restores it into a database it
did not come from.

It starts no agent reconciler, so it is safe to run on a host that is already
serving a panel.

It is not decoration. Its first run found that the plane could not take a
snapshot at all, and then two more defects behind that one — all three recorded
in `docs/features/plane-disaster-recovery.md`. Nothing else in the repository
could have found them, because every other test used a fake schema.

## Releasing

Tagging builds the binaries and creates a **draft** release. It stays a draft —
invisible to everyone — until it is signed:

```sh
export CYPHER_RELEASE_SIGNING_KEY="$(cat cypher-release.key)"
make release-sign VERSION=v0.1.0
```

That does not take the draft's word for anything. It checks out the tag from
the repository, **rebuilds both binaries from source** with the same flags CI
used, and compares them byte for byte against what the draft published — then
signs a manifest computed from its own rebuild, verifies that signature, uploads
it, and only then flips the release out of draft.

The rebuild is the whole point. Downloading the binaries and the `SHA256SUMS`
from the same draft and confirming they agree proves nothing: whoever can write
to the draft writes both, and they agree perfectly. That check is circular, and
trusting it would have had the offline key turn CI-supplied bytes into an
authentic fleet release — precisely the compromise the offline key exists to
survive. Comparing against a local rebuild makes the signature mean *"I built
this from source I read"* rather than *"CI agrees with itself"*.

Go builds are reproducible under `-trimpath` with `CGO_ENABLED=0` and fixed
ldflags, which is what lets a mismatch carry meaning. The script refuses to run
on a different Go toolchain than the release was built with, because a version
skew is indistinguishable from a tampered artifact once the comparison fails —
and an operator who learns that mismatches are normal is an operator who will
sign through a real one.

## Verifying a release by hand

```sh
gh release download v0.1.0 -p 'SHA256SUMS*' -p 'cypher*'
go run -C core ./cmd/release-sign -verify \
  -in SHA256SUMS -sig SHA256SUMS.sig -key "$RELEASE_PUBKEY"
sha256sum -c SHA256SUMS
```

Signature first, digests second. The checksums prove the download arrived
intact; only the signature says the release is ours.

## Status

The release pipeline builds and drafts; signing is offline and manual, so an
unsigned release cannot reach anyone.

**Agent-side verification is implemented** (`agent/updater`): the manifest's
signature is checked against the baked-in key list before any artifact is
fetched, the downloaded binary is checked against the signed digest before it is
ever renamed, and ADR-010 §4–5's pre-flight, two-slot swap and self-rollback are
in place with tests that assert each refusal leaves the running binary alone.

**What is missing is the key, not the check.** `release-pubkey.txt` is empty, so
a build made from this tree trusts no key
— and an updater with nothing to verify against does not run: it reports
`disabled` in the panel's fleet table, naming the reason, and never renames a
file. That is the opposite of a stubbed check, and it is what ADR-010 §3
requires; there is deliberately **no flag that skips verification**, because a
skip flag is the hole everything else leaks through.

The key must exist before the first release regardless: a release published
unsigned can never be retroactively covered by one.
