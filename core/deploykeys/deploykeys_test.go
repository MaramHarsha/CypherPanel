package deploykeys

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	created []domain.DeployKey
	deleted []string
	// inUse maps a key id to the applications referencing it; a delete of such
	// a key is refused the way the RESTRICT foreign key refuses it.
	inUse map[string][]domain.ApplicationRef
}

func (f *fakeStore) ListApplicationsByDeployKey(_ context.Context, keyID string) ([]domain.ApplicationRef, error) {
	return f.inUse[keyID], nil
}

func (f *fakeStore) CreateDeployKey(_ context.Context, dk domain.DeployKey) (domain.DeployKey, error) {
	f.created = append(f.created, dk)
	return dk, nil
}

func (f *fakeStore) GetDeployKey(_ context.Context, id string) (domain.DeployKey, error) {
	for _, dk := range f.created {
		if dk.ID == id {
			return dk, nil
		}
	}
	return domain.DeployKey{}, store.ErrNotFound
}

func (f *fakeStore) ListDeployKeys(context.Context) ([]domain.DeployKey, error) {
	return append([]domain.DeployKey(nil), f.created...), nil
}

func (f *fakeStore) DeleteDeployKey(_ context.Context, id string) error {
	if len(f.inUse[id]) > 0 {
		return store.ErrInUse
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeSealer records what it was asked to seal and returns a marked
// ciphertext, so tests can assert the stored bytes are not the plaintext.
type fakeSealer struct{ sealed [][]byte }

func (f *fakeSealer) Seal(plaintext []byte) ([]byte, []byte, error) {
	f.sealed = append(f.sealed, append([]byte(nil), plaintext...))
	return append([]byte("sealed:"), plaintext...), []byte("nonce"), nil
}

func TestCreateGeneratesSealedEd25519Key(t *testing.T) {
	fs, sealer := &fakeStore{}, &fakeSealer{}
	svc := NewService(fs, sealer)

	dk, err := svc.Create(context.Background(), "  ci key  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if dk.Name != "ci key" {
		t.Errorf("name = %q, want trimmed %q", dk.Name, "ci key")
	}
	if !strings.HasPrefix(dk.ID, "dk_") {
		t.Errorf("id = %q, want dk_ prefix", dk.ID)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(dk.PublicKey))
	if err != nil {
		t.Fatalf("public key %q does not parse: %v", dk.PublicKey, err)
	}
	if pub.Type() != ssh.KeyAlgoED25519 {
		t.Errorf("key type = %s, want %s", pub.Type(), ssh.KeyAlgoED25519)
	}
	if got, want := dk.Fingerprint, ssh.FingerprintSHA256(pub); got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}
	// The private key reaches the store only through the sealer (rule 20 /
	// deploy-key-private-repos.md §1): the sealer saw the PEM, and the stored
	// ciphertext is the sealer's output, not the plaintext.
	if len(sealer.sealed) != 1 || !bytes.Contains(sealer.sealed[0], []byte("OPENSSH PRIVATE KEY")) {
		t.Fatalf("sealer payloads = %d, want exactly the private-key PEM", len(sealer.sealed))
	}
	if bytes.Equal(dk.PrivateKeyCT, sealer.sealed[0]) {
		t.Error("stored ciphertext equals the plaintext PEM — key not sealed")
	}
	if len(dk.PrivateKeyNonce) == 0 {
		t.Error("nonce not stored")
	}
}

func TestCreateValidatesName(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeSealer{})
	for name, input := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"too long":   strings.Repeat("x", 101),
	} {
		_, err := svc.Create(context.Background(), input)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
}

func TestDeleteMissingKeyReportsNotFound(t *testing.T) {
	fs := &fakeStore{}
	svc := NewService(fs, &fakeSealer{})
	if err := svc.Delete(context.Background(), "dk_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if len(fs.deleted) != 0 {
		t.Fatal("delete reached the store for a missing key")
	}
}

func TestDeleteExistingKey(t *testing.T) {
	fs := &fakeStore{}
	svc := NewService(fs, &fakeSealer{})
	dk, err := svc.Create(context.Background(), "gone soon")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(context.Background(), dk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fs.deleted) != 1 || fs.deleted[0] != dk.ID {
		t.Fatalf("deleted = %v, want [%s]", fs.deleted, dk.ID)
	}
}

// TestDeleteInUseNamesTheBlockingApplications: a RESTRICTed delete comes back
// as an InUseError listing the applications by id and name, and still matches
// store.ErrInUse for callers that only map the status
// (deploy-key-private-repos.md §3; control-plane-hardening.md §8).
func TestDeleteInUseNamesTheBlockingApplications(t *testing.T) {
	fs := &fakeStore{}
	svc := NewService(fs, &fakeSealer{})
	dk, err := svc.Create(context.Background(), "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fs.inUse = map[string][]domain.ApplicationRef{dk.ID: {{ID: "app_1", Name: "web"}, {ID: "app_2", Name: "api"}}}

	err = svc.Delete(context.Background(), dk.ID)
	if !errors.Is(err, store.ErrInUse) {
		t.Fatalf("err = %v, want one matching store.ErrInUse", err)
	}
	var inUse *InUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %T, want *InUseError", err)
	}
	if len(inUse.Applications) != 2 || inUse.Applications[0].Name != "web" || inUse.Applications[1].ID != "app_2" {
		t.Fatalf("blockers = %+v", inUse.Applications)
	}
	if !strings.Contains(err.Error(), "web, api") {
		t.Fatalf("message %q does not name the applications", err.Error())
	}
	if len(fs.deleted) != 0 {
		t.Fatal("the key was recorded as deleted despite the refusal")
	}
}
