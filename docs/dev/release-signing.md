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

## The key is offline

The private key is generated on a machine that is not CI and never leaves it in
plaintext. CI holds it for the duration of one step, from a repository secret,
and cannot generate one — a missing key fails the release rather than publishing
unsigned binaries.

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

1. Add the private key as the repository secret **`CYPHER_RELEASE_SIGNING_KEY`**
   (base64, no newline). Nothing else in the repository may read it — it is used
   by exactly one step in `release.yml`.
2. Record the public key below, and bake it into the agent when ADR-010's
   update mechanism is implemented.

```
RELEASE_PUBKEY = <not yet generated — see "Status" below>
```

## Verifying a release by hand

```sh
gh release download v0.1.0 -p 'SHA256SUMS*' -p 'cypher*'
sha256sum -c SHA256SUMS
```

To check the signature, decode `SHA256SUMS.sig` (base64) and verify the raw
Ed25519 signature over the bytes of `SHA256SUMS` against `RELEASE_PUBKEY`.

## Status

The release pipeline signs, and refuses to publish unsigned. **Agent-side
verification is not implemented yet** — the two-slot swap, the baked-in public
key, and the self-rollback described in ADR-010 §4–5 land with the auto-update
mechanism. Until then the signature protects operators who verify by hand, and
the key must exist before the first release regardless: a release published
unsigned can never be retroactively covered by one.
