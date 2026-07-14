package webserver

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// go test ./internal/webserver -update  regenerates the golden files.
var update = flag.Bool("update", false, "update golden files")

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("output does not match %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestNginxRenderGolden(t *testing.T) {
	got, err := Nginx{}.Render(VHostSpec{
		Domain:    "alice.example.com",
		Aliases:   []string{"www.alice.example.com"},
		WebRoot:   "/home/cyph_alice/public_html",
		PHPSocket: "/run/cypherpanel/php-cyph_alice.sock",
		AccessLog: "/home/cyph_alice/logs/alice.example.com.access.log",
		ErrorLog:  "/home/cyph_alice/logs/alice.example.com.error.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "vhost.golden", got)
}

func TestNginxRenderTLSGolden(t *testing.T) {
	got, err := Nginx{}.Render(VHostSpec{
		Domain:      "alice.example.com",
		Aliases:     []string{"www.alice.example.com"},
		WebRoot:     "/home/cyph_alice/public_html",
		PHPSocket:   "/run/cypherpanel/php-cyph_alice.sock",
		AccessLog:   "/home/cyph_alice/logs/alice.example.com.access.log",
		ErrorLog:    "/home/cyph_alice/logs/alice.example.com.error.log",
		TLSCertPath: "/var/lib/cypherpanel/ssl/alice.example.com/fullchain.pem",
		TLSKeyPath:  "/var/lib/cypherpanel/ssl/alice.example.com/privkey.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "vhost_tls.golden", got)
}

func TestPHPFPMPoolRenderGolden(t *testing.T) {
	got, err := RenderPHPFPMPool(PoolSpec{
		User:          "cyph_alice",
		Socket:        "/run/cypherpanel/php-cyph_alice.sock",
		WebServerUser: "www-data",
		MaxChildren:   8,
		AdminValues: map[string]string{
			"memory_limit":        "512M",
			"upload_max_filesize": "64M",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "pool.golden", got)
}

func TestRenderRejectsIncompleteSpec(t *testing.T) {
	if _, err := (Nginx{}).Render(VHostSpec{Domain: "x"}); err == nil {
		t.Fatal("expected error for vhost spec missing web root / socket")
	}
	if _, err := RenderPHPFPMPool(PoolSpec{User: "x"}); err == nil {
		t.Fatal("expected error for pool spec missing socket")
	}
}
