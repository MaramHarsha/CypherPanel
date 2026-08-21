package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// A transport failure must not carry the webhook URL (itself the secret) into
// the returned error, which fanOut and the test handler log (notifications.md
// §6, ENGINEERING rule 20).
func TestDeliverRedactsWebhookURLOnTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	secretURL := srv.URL + "/services/SECRET-TOKEN-1234"
	srv.Close() // listener gone → connection refused, a transport error

	m := New(&mgrStore{}, identityOpener{}, quietLog())
	n := webhookNotifier("ntf_1", domain.NotifyChannelSlack, secretURL, domain.EventDeployFailed)
	ev := domain.NotifyEvent{Type: domain.EventDeployFailed, Title: "T", Body: "B"}

	err := m.Deliver(context.Background(), n, ev)
	if err == nil {
		t.Fatal("expected a transport error from an unreachable endpoint")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN-1234") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaked the webhook URL: %v", err)
	}
}

func TestSanitizeHeaderStripsCRLF(t *testing.T) {
	got := SanitizeHeader("Deploy failed: app\r\nBcc: evil@example.com")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("sanitizeHeader left CR/LF in %q", got)
	}
}
