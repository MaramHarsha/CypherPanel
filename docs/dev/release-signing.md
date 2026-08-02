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
2. Record the public key below, and bake it into the agent when ADR-010's
   update mechanism is implemented.

```
RELEASE_PUBKEY = <not yet generated — see "Status" below>
```

## Releasing

Tagging builds the binaries and creates a **draft** release. It stays a draft —
invisible to everyone — until it is signed:

```sh
export CYPHER_RELEASE_SIGNING_KEY="$(cat cypher-release.key)"
make release-sign VERSION=v0.1.0
```

That downloads the draft's artifacts, re-checks every digest against
`SHA256SUMS`, signs the manifest, verifies the signature it just made, uploads
`SHA256SUMS.sig`, and only then flips the release out of draft.

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
unsigned release cannot reach anyone. **Agent-side
verification is not implemented yet** — the two-slot swap, the baked-in public
key, and the self-rollback described in ADR-010 §4–5 land with the auto-update
mechanism. Until then the signature protects operators who verify by hand, and
the key must exist before the first release regardless: a release published
unsigned can never be retroactively covered by one.
