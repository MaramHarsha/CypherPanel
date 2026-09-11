package engine

// SaveImage/LoadImage tests (builder-role-and-relay.md §3): the relay's
// daemon-side endpoints, including the literal-path rule for namespaced refs
// and the JSON-lines error surfacing a corrupt tar produces.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSaveImageStreamsTarWithLiteralPath(t *testing.T) {
	tar := []byte("fake-image-tar-bytes")
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/images/cypher/app1:rev1/get": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/x-tar")
			_, _ = w.Write(tar)
		},
	})
	rc, err := m.client().SaveImage(context.Background(), "cypher/app1:rev1")
	if err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, tar) {
		t.Fatalf("streamed %q (err %v), want the tar", got, err)
	}
}

func TestSaveImageMissingImageIsError(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/images/gone:latest/get": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such image: gone:latest"}`))
		},
	})
	if _, err := m.client().SaveImage(context.Background(), "gone:latest"); err == nil {
		t.Fatal("SaveImage of a missing image must error")
	}
}

func TestLoadImageSendsTar(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/images/load": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"stream":"Loaded image: cypher/app1:rev1\n"}`))
		},
	})
	if err := m.client().LoadImage(context.Background(), strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage: %v", err)
	}
	req := m.lastTo(t, "/images/load")
	if string(req.body) != "tar-bytes" || req.query.Get("quiet") != "1" {
		t.Fatalf("load request = %+v, want the tar body with quiet=1", req)
	}
}

// A corrupt/truncated tar fails after a 200 header via an error record in the
// progress stream — it must surface as an error, never a silent success
// (spec §6: a failed load yields no tag, so a retry stays honest).
func TestLoadImageSurfacesStreamError(t *testing.T) {
	m := newMockDaemon(t, map[string]http.HandlerFunc{
		"/images/load": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"error":"unexpected EOF"}`))
		},
	})
	err := m.client().LoadImage(context.Background(), strings.NewReader("truncated"))
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("LoadImage err = %v, want the stream error surfaced", err)
	}
}
