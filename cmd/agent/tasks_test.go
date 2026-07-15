package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/paths"
	"github.com/MaramHarsha/CypherPanel/internal/platform"
	"github.com/MaramHarsha/CypherPanel/internal/webserver"
)

type sitesOp struct {
	kind        string // "provision" | "removepool"
	poolVersion string // for removepool
	spec        platform.SiteSpec
}

type fakeSites struct{ ops []sitesOp }

func (f *fakeSites) Provision(_ context.Context, spec platform.SiteSpec) error {
	f.ops = append(f.ops, sitesOp{kind: "provision", spec: spec})
	return nil
}
func (f *fakeSites) Deprovision(context.Context, string, string) error { return nil }
func (f *fakeSites) RemovePHPPool(_ context.Context, _, version string) error {
	f.ops = append(f.ops, sitesOp{kind: "removepool", poolVersion: version})
	return nil
}
func (f *fakeSites) InstallCertificate(context.Context, string, []byte, string, []byte) error {
	return nil
}
func (f *fakeSites) ApplyVHost(context.Context, string, []byte) error { return nil }

type fakeVHost struct{ last webserver.VHostSpec }

func (f *fakeVHost) Name() string { return "fake" }
func (f *fakeVHost) Render(spec webserver.VHostSpec) ([]byte, error) {
	f.last = spec
	return []byte("vhost"), nil
}

func newExec(t *testing.T) (*taskExecutor, *fakeSites, *fakeVHost) {
	t.Helper()
	l := paths.ForFamily(paths.FamilyDebian)
	l.SSLDir = t.TempDir() // isolate cert lookups from the real FS
	fs := &fakeSites{}
	fv := &fakeVHost{}
	return &taskExecutor{layout: l, sites: fs, vhost: fv}, fs, fv
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestChangePHPVersion_RemovesOldPoolBeforeProvisioningNew(t *testing.T) {
	e, fs, _ := newExec(t)
	raw := mustJSON(t, jobs.PHPVersionChangePayload{
		Username: "cyph_x", Domain: "x.example.com",
		OldPHPVersion: "8.2", NewPHPVersion: "8.4",
	})
	if err := e.changePHPVersion(context.Background(), raw); err != nil {
		t.Fatalf("changePHPVersion: %v", err)
	}
	if len(fs.ops) != 2 {
		t.Fatalf("ops = %+v, want 2 (removepool, provision)", fs.ops)
	}
	// Ordering is the crux: the old pool must be removed (releasing the shared
	// account socket) BEFORE the new version's pool is written.
	if fs.ops[0].kind != "removepool" || fs.ops[0].poolVersion != "8.2" {
		t.Fatalf("first op = %+v, want removepool of 8.2", fs.ops[0])
	}
	if fs.ops[1].kind != "provision" || fs.ops[1].spec.PHPVersion != "8.4" {
		t.Fatalf("second op = %+v, want provision of 8.4", fs.ops[1])
	}
}

func TestChangePHPVersion_SameVersionSkipsPoolRemoval(t *testing.T) {
	e, fs, _ := newExec(t)
	raw := mustJSON(t, jobs.PHPVersionChangePayload{
		Username: "cyph_x", Domain: "x.example.com",
		OldPHPVersion: "8.3", NewPHPVersion: "8.3",
	})
	if err := e.changePHPVersion(context.Background(), raw); err != nil {
		t.Fatalf("changePHPVersion: %v", err)
	}
	if len(fs.ops) != 1 || fs.ops[0].kind != "provision" {
		t.Fatalf("ops = %+v, want a single provision (no pool removal)", fs.ops)
	}
}

func TestApplySite_PreservesHTTPSWhenCertPresent(t *testing.T) {
	e, _, fv := newExec(t)
	domain := "secure.example.com"
	writeSelfSignedCert(t, e.layout.SSLCertPath(domain))

	// A plain site.provision (e.g. triggered by an INI or version change) must
	// NOT drop an already-HTTPS site back to plain HTTP.
	raw := mustJSON(t, jobs.SiteProvisionPayload{
		Username: "cyph_x", Domain: domain, PHPVersion: "8.3",
	})
	if err := e.provisionSite(context.Background(), raw); err != nil {
		t.Fatalf("provisionSite: %v", err)
	}
	if !fv.last.TLSEnabled() {
		t.Fatal("re-provisioning a site with an installed cert must keep TLS enabled")
	}
}

func TestApplySite_PlainHTTPWhenNoCert(t *testing.T) {
	e, _, fv := newExec(t)
	raw := mustJSON(t, jobs.SiteProvisionPayload{
		Username: "cyph_x", Domain: "plain.example.com", PHPVersion: "8.3",
	})
	if err := e.provisionSite(context.Background(), raw); err != nil {
		t.Fatalf("provisionSite: %v", err)
	}
	if fv.last.TLSEnabled() {
		t.Fatal("a site without a cert must render plain HTTP")
	}
}

// writeSelfSignedCert writes a minimal, currently-valid self-signed cert to
// path so acme.CertValidUntil parses a non-zero expiry.
func writeSelfSignedCert(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
}
