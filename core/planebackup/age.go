package planebackup

// The encryption (plane-disaster-recovery.md §4).
//
// ASYMMETRIC, and that choice is the crux of the whole feature. Three designs
// were considered and two rejected:
//
//   - LEAVE THE KEY OUT and tell the operator to keep it. The panel already
//     does this and nobody does it: the failure is silent, total, and
//     discovered on the one day it matters, after a year of green checkmarks. A
//     recovery story whose critical prerequisite is an unenforced comment is a
//     recovery story that does not exist.
//   - SEAL THE ARCHIVE WITH THE MASTER KEY. Correct for the local upgrade
//     snapshot, which never leaves the host. Off-host it is circular: you need
//     the key to open the archive that contains the key.
//
// So: the plane stores a RECIPIENT — a public key — and can only ever write.
// The private half is generated once, shown once, and never stored by the
// panel. There is no API that returns it and no configuration variable that
// holds it.
//
// The format is age, not something of ours, and that is deliberate: an operator
// recovering from a lost panel can decrypt with a tool they can install from
// anywhere, rather than needing a working copy of the very binary they are
// trying to restore.

import (
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// AgeCrypto is the archive's encryption.
type AgeCrypto struct{}

// GenerateRecoveryKey mints an identity. The private half is returned ONCE and
// this package never writes it anywhere — the caller shows it to the operator
// and forgets it too.
func GenerateRecoveryKey() (recipient, identity string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", fmt.Errorf("planebackup: generating a recovery key: %w", err)
	}
	return id.Recipient().String(), id.String(), nil
}

// ValidRecipient reports whether a stored recipient is one this can write to.
// Checked when it is set rather than at 03:00 on the first nightly run.
func ValidRecipient(s string) bool {
	_, err := age.ParseX25519Recipient(strings.TrimSpace(s))
	return err == nil
}

func (AgeCrypto) Wrap(w io.Writer, recipient string) (io.WriteCloser, error) {
	r, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return nil, fmt.Errorf("planebackup: the stored recovery recipient is not an age public key: %w", err)
	}
	return age.Encrypt(w, r)
}

func (AgeCrypto) Unwrap(r io.Reader, identity string) (io.Reader, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(identity))
	if err != nil {
		return nil, fmt.Errorf("planebackup: that is not a recovery key: %w", err)
	}
	return age.Decrypt(r, id)
}
