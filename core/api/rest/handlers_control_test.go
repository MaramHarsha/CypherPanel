package rest

// Deployment control at the HTTP edge (deployment-control.md): the two verbs,
// the two refusals they must tell apart, and the log window.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/scheduler"
)

// openLogStream opens an SSE log stream and hangs up once the headers are in,
// which is all these tests need: the handler resolves the window and subscribes
// BEFORE it writes its first frame, so the window is recorded by the time the
// status line arrives. doJSON cannot be used — it reads to EOF, and a healthy
// stream has none.
func openLogStream(t *testing.T, ts *httptest.Server, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		return res.StatusCode, nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return res.StatusCode, body
}

func TestCancelDeploymentEndsIt(t *testing.T) {
	ts, _, _ := newTestServerControl(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/cancel", token, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var dep struct {
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &dep); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if dep.Status != string(domain.DeployFailed) {
		t.Fatalf("status = %q, want failed — a cancelled deploy is one that did not ship", dep.Status)
	}
	// Attributed to the caller by address, so the deployment's own detail says
	// who stopped it without a second lookup.
	if !strings.Contains(dep.Detail, testEmail) {
		t.Fatalf("detail = %q, want it to name the caller", dep.Detail)
	}
}

// The two 409s are different situations and must read differently: one has a
// recovery (roll back), the other has nothing to do.
func TestCancelDeploymentRefusals(t *testing.T) {
	cases := []struct {
		name   string
		status domain.DeploymentStatus
		says   string
	}{
		{"rolling out points at rollback", domain.DeployRollingOut, "roll back"},
		{"finished says so", domain.DeploySucceeded, "already finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, deployer, _ := newTestServerControl(t)
			deployer.cancelErr = scheduler.ErrCannotCancel
			deployer.cancelStatus = tc.status
			token := login(t, ts)

			status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/cancel", token, "")
			if status != http.StatusConflict {
				t.Fatalf("status = %d body %s, want 409", status, body)
			}
			if !strings.Contains(string(body), tc.says) {
				t.Fatalf("body = %s, want it to mention %q", body, tc.says)
			}
		})
	}
}

func TestRestartApplication(t *testing.T) {
	ts, deployer, _ := newTestServerControl(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/applications/app_x/restart", token, "")
	if status != http.StatusAccepted {
		t.Fatalf("status = %d body %s", status, body)
	}
	// An application, not a deployment: a restart ships nothing, and returning
	// a deployment would say it had.
	if strings.Contains(string(body), `"revision_id"`) {
		t.Fatalf("body = %s, want the application rather than a deployment", body)
	}
	if got := deployer.restarted; len(got) != 1 || got[0] != "app_x" {
		t.Fatalf("restarted = %v", got)
	}
}

func TestRestartRefusedBeforeTheFirstDeploy(t *testing.T) {
	ts, deployer, _ := newTestServerControl(t)
	deployer.restartErr = scheduler.ErrNeverDeployed
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/applications/app_x/restart", token, "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", status, body)
	}
	if !strings.Contains(string(body), "deploy it first") {
		t.Fatalf("body = %s, want it to say what to do instead", body)
	}
}

// ─── the log window (§4) ────────────────────────────────────────────────────

// A relative window resolves to an instant the stream can start from.
func TestLogSinceAcceptsADuration(t *testing.T) {
	ts, _, logs := newTestServerControl(t)
	token := login(t, ts)

	before := time.Now()
	if status, body := openLogStream(t, ts, "/api/v1/applications/app_x/logs?since=15m", token); status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	logs.mu.Lock()
	got := logs.lastSince
	logs.mu.Unlock()
	if got.IsZero() {
		t.Fatal("no window reached the stream")
	}
	if delta := before.Sub(got); delta < 14*time.Minute || delta > 16*time.Minute {
		t.Fatalf("window is %s before now, want about 15m", delta)
	}
}

func TestLogSinceAcceptsATimestamp(t *testing.T) {
	ts, _, logs := newTestServerControl(t)
	token := login(t, ts)

	want := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	if status, body := openLogStream(t, ts,
		"/api/v1/applications/app_x/logs?since=2026-09-06T09:00:00Z", token); status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	logs.mu.Lock()
	got := logs.lastSince
	logs.mu.Unlock()
	if !got.Equal(want) {
		t.Fatalf("window = %s, want %s", got, want)
	}
}

// Omitted means the whole retained window, which is the zero time.
func TestLogWithoutSinceReplaysEverything(t *testing.T) {
	ts, _, logs := newTestServerControl(t)
	token := login(t, ts)

	if status, body := openLogStream(t, ts, "/api/v1/applications/app_x/logs", token); status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	logs.mu.Lock()
	got := logs.lastSince
	logs.mu.Unlock()
	if !got.IsZero() {
		t.Fatalf("window = %s, want the zero time", got)
	}
}

// A client that meant "the last minute" and silently got four hours has been
// given the wrong answer confidently.
func TestUnparseableSinceIsRefused(t *testing.T) {
	ts, _, _ := newTestServerControl(t)
	token := login(t, ts)

	for _, bad := range []string{"yesterday", "15", "-15m", "0s", "9000h", "2026-09-06"} {
		status, body := openLogStream(t, ts, "/api/v1/applications/app_x/logs?since="+bad, token)
		if status != http.StatusBadRequest {
			t.Errorf("since=%s: status = %d body %s, want 400", bad, status, body)
		}
	}
}

// The build-log stream takes the same parameter, and gets it wrong in the same
// way if it does not.
func TestDeploymentLogsTakeSinceToo(t *testing.T) {
	ts, _, logs := newTestServerControl(t)
	token := login(t, ts)

	if status, body := openLogStream(t, ts, "/api/v1/deployments/dep_test/logs?since=5m", token); status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	logs.mu.Lock()
	got := logs.lastSince
	logs.mu.Unlock()
	if got.IsZero() {
		t.Fatal("no window reached the build-log stream")
	}
}
