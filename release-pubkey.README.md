# release-pubkey.txt

The ed25519 public key(s) a released `cypher-agent` will trust for its own
updates (ADR-010 §3, [docs/dev/release-signing.md](docs/dev/release-signing.md)).

It lives **in the repository, in the tag**, rather than in a CI variable, for
two reasons:

- **Reproducibility.** `scripts/release-sign.sh` rebuilds every binary from the
  tag and compares byte-for-byte against what CI published. A value that came
  from CI settings could not be reproduced from a checkout, so the comparison —
  the whole point of the offline key — would be impossible.
- **Auditability.** The key a release trusts is then part of the release's own
  source, visible in `git log`, and rotating it is a reviewable commit rather
  than a settings change nobody sees.

The key is PUBLIC. The private half never leaves the signing machine and is
never in this repository or in CI (release-signing.md).

## Format

One or two base64 (standard encoding) keys, comma-separated, on one line. Two is
what rotation needs and the ceiling the parser enforces: release N is signed
with A and ships `A,B`; N+1 is signed with B; N+2 ships `B` alone.

**Empty is the current state and is honest, not broken.** With no key an agent
can verify nothing, so its updater reports `disabled` in the fleet table naming
the reason and never renames a file — which is what ADR-010 §3 requires. It is a
real check failing closed, not a stubbed one passing open.
