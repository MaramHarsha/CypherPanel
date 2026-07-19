package proxy_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MaramHarsha/cypherpanel/agent/proxy"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

func TestTraefikWriter(t *testing.T) {
	dir := t.TempDir()
	w := proxy.NewTraefikWriter(dir)
	ctx := context.Background()

	upstream, ok, err := w.Route(ctx, "app1")
	if err != nil {
		t.Fatalf("Route err: %v", err)
	}
	if ok {
		t.Fatalf("Expected not ok, got ok with upstream %s", upstream)
	}

	spec := &agentv1.RouteSpec{
		Domain:     "example.com",
		PathPrefix: "/api",
		Https:      true,
	}

	if err := w.SetRoute(ctx, "app1", spec, "10.0.0.1:8080"); err != nil {
		t.Fatalf("SetRoute err: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "app1.yml"))
	if err != nil {
		t.Fatalf("ReadFile err: %v", err)
	}
	content := string(b)
	if !contains(content, "example.com") || !contains(content, "10.0.0.1:8080") || !contains(content, "certResolver") {
		t.Fatalf("File missing expected content:\n%s", content)
	}

	upstream, ok, err = w.Route(ctx, "app1")
	if err != nil {
		t.Fatalf("Route err: %v", err)
	}
	if !ok {
		t.Fatalf("Expected ok, got not ok")
	}
	if upstream != "10.0.0.1:8080" {
		t.Fatalf("Expected 10.0.0.1:8080, got %s", upstream)
	}

	if err := w.RemoveRoute(ctx, "app1"); err != nil {
		t.Fatalf("RemoveRoute err: %v", err)
	}

	_, ok, _ = w.Route(ctx, "app1")
	if ok {
		t.Fatalf("Expected not ok after remove")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr || (len(s) > len(substr) && contains(s[1:], substr))
}
