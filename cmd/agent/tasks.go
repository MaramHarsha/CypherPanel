package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/acme"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/paths"
	"github.com/MaramHarsha/CypherPanel/internal/platform"
	"github.com/MaramHarsha/CypherPanel/internal/webserver"
)

// taskExecutor dispatches queued tasks to their handlers. Every handler must
// be idempotent — JetStream may deliver the same task more than once.
type taskExecutor struct {
	layout paths.Layout
	users  platform.SystemUsers
	sites  platform.Sites
	vhost  webserver.VHostRenderer
	acme   *acme.Issuer
}

// Handle runs a task and returns optional result metadata (reported back with
// the outcome) plus an error (nil = success).
func (e *taskExecutor) Handle(ctx context.Context, t jobs.Task) (map[string]string, error) {
	switch t.Type {
	case jobs.TypeNoop:
		return nil, nil

	case jobs.TypeSystemUserCreate:
		var p jobs.SystemUserCreatePayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
		}
		if p.Username == "" {
			return nil, jobs.Permanent(errors.New("username is required"))
		}
		home := p.HomeDir
		if home == "" {
			home = e.layout.AccountHome(p.Username)
		}
		err := e.users.Create(ctx, p.Username, home)
		if errors.Is(err, platform.ErrUnsupported) {
			return nil, jobs.Permanent(err)
		}
		return nil, err

	case jobs.TypeSystemUserRemove:
		var p jobs.SystemUserRemovePayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
		}
		if p.Username == "" {
			return nil, jobs.Permanent(errors.New("username is required"))
		}
		err := e.users.Remove(ctx, p.Username)
		if errors.Is(err, platform.ErrUnsupported) {
			return nil, jobs.Permanent(err)
		}
		return nil, err

	case jobs.TypeSiteProvision:
		return nil, e.provisionSite(ctx, t.Payload)

	case jobs.TypeSiteDeprovision:
		return nil, e.deprovisionSite(ctx, t.Payload)

	case jobs.TypeSSLIssue:
		return e.issueSSL(ctx, t.Payload)

	default:
		// Unknown type: this agent build is older than the control plane.
		// Permanent-fail so it surfaces instead of retrying forever.
		return nil, jobs.Permanent(fmt.Errorf("unknown task type %q (agent version %s)", t.Type, version))
	}
}

// provisionSite renders this account's nginx vhost + PHP-FPM pool and applies
// them (dirs owned by the account user, configs validated + reloaded).
func (e *taskExecutor) provisionSite(ctx context.Context, raw []byte) error {
	var p jobs.SiteProvisionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if p.Username == "" || p.Domain == "" || p.PHPVersion == "" {
		return jobs.Permanent(errors.New("username, domain and php_version are required"))
	}

	webRoot := e.layout.AccountWebRoot(p.Username)
	logDir := e.layout.AccountLogDir(p.Username)
	socket := e.layout.PHPFPMSocketPath(p.Username)

	vhostCfg, err := e.vhost.Render(webserver.VHostSpec{
		Domain:    p.Domain,
		WebRoot:   webRoot,
		PHPSocket: socket,
		AccessLog: filepath.Join(logDir, p.Domain+".access.log"),
		ErrorLog:  filepath.Join(logDir, p.Domain+".error.log"),
	})
	if err != nil {
		return jobs.Permanent(err)
	}

	// Package memory limit is the baseline; per-account INI overrides (already
	// allowlist-validated by Core) win where set.
	admin := map[string]string{}
	if p.MemoryMB > 0 {
		admin["memory_limit"] = fmt.Sprintf("%dM", p.MemoryMB)
	}
	for k, v := range p.PHPSettings {
		admin[k] = v
	}
	poolCfg, err := webserver.RenderPHPFPMPool(webserver.PoolSpec{
		User:          p.Username,
		Socket:        socket,
		WebServerUser: e.layout.WebServerUser,
		AdminValues:   admin,
	})
	if err != nil {
		return jobs.Permanent(err)
	}

	spec := platform.SiteSpec{
		Username:    p.Username,
		AccountDirs: []string{webRoot, logDir},
		VHostPath:   e.layout.VhostConfPath(p.Domain),
		VHostConfig: vhostCfg,
		PoolPath:    e.layout.PHPFPMPoolPath(p.PHPVersion, p.Username),
		PoolConfig:  poolCfg,
	}
	err = e.sites.Provision(ctx, spec)
	if errors.Is(err, platform.ErrUnsupported) {
		return jobs.Permanent(err)
	}
	return err
}

func (e *taskExecutor) deprovisionSite(ctx context.Context, raw []byte) error {
	var p jobs.SiteDeprovisionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if p.Username == "" || p.Domain == "" || p.PHPVersion == "" {
		return jobs.Permanent(errors.New("username, domain and php_version are required"))
	}
	err := e.sites.Deprovision(ctx,
		e.layout.VhostConfPath(p.Domain),
		e.layout.PHPFPMPoolPath(p.PHPVersion, p.Username))
	if errors.Is(err, platform.ErrUnsupported) {
		return jobs.Permanent(err)
	}
	return err
}

// issueSSL obtains a certificate via ACME and switches the site's vhost to
// HTTPS. Idempotent: a still-valid cert (>30 days) is a no-op, so redelivery
// or scheduled renewal doesn't burn ACME rate limits.
func (e *taskExecutor) issueSSL(ctx context.Context, raw []byte) (map[string]string, error) {
	var p jobs.SSLIssuePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if p.Username == "" || p.Domain == "" || p.Email == "" {
		return nil, jobs.Permanent(errors.New("username, domain and email are required"))
	}
	if e.acme == nil {
		return nil, jobs.Permanent(errors.New("acme issuer not configured"))
	}

	certPath := e.layout.SSLCertPath(p.Domain)
	keyPath := e.layout.SSLKeyPath(p.Domain)

	// Idempotency: skip re-issue if the installed cert is valid for >30 days.
	if until := acme.CertValidUntil(certPath); !until.IsZero() && time.Until(until) > 30*24*time.Hour {
		return map[string]string{jobs.MetaSSLNotAfter: until.UTC().Format(time.RFC3339)}, nil
	}

	res, err := e.acme.Obtain(p.Domain, p.Email, e.layout.AccountWebRoot(p.Username))
	if err != nil {
		return nil, err // transient (network, propagation) → retryable
	}

	if err := e.sites.InstallCertificate(ctx, certPath, res.CertPEM, keyPath, res.KeyPEM); err != nil {
		if errors.Is(err, platform.ErrUnsupported) {
			return nil, jobs.Permanent(err)
		}
		return nil, err
	}

	// Regenerate the vhost with TLS and reload.
	vhostCfg, err := e.vhost.Render(webserver.VHostSpec{
		Domain:      p.Domain,
		WebRoot:     e.layout.AccountWebRoot(p.Username),
		PHPSocket:   e.layout.PHPFPMSocketPath(p.Username),
		AccessLog:   filepath.Join(e.layout.AccountLogDir(p.Username), p.Domain+".access.log"),
		ErrorLog:    filepath.Join(e.layout.AccountLogDir(p.Username), p.Domain+".error.log"),
		TLSCertPath: certPath,
		TLSKeyPath:  keyPath,
	})
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	if err := e.sites.ApplyVHost(ctx, e.layout.VhostConfPath(p.Domain), vhostCfg); err != nil {
		if errors.Is(err, platform.ErrUnsupported) {
			return nil, jobs.Permanent(err)
		}
		return nil, err
	}

	return map[string]string{jobs.MetaSSLNotAfter: res.NotAfter.UTC().Format(time.RFC3339)}, nil
}

// reportResult delivers the outcome to CypherCore over gRPC, retrying
// briefly — a lost result would leave the task 'pending' forever.
func reportResult(client agentv1.AgentServiceClient, serverID string) jobs.ResultReporter {
	return func(ctx context.Context, t jobs.Task, meta map[string]string, taskErr error) {
		req := &agentv1.ReportTaskResultRequest{
			ServerId: serverID,
			TaskId:   t.ID,
			Status:   agentv1.TaskStatus_TASK_STATUS_SUCCEEDED,
			Metadata: meta,
		}
		if taskErr != nil {
			req.Status = agentv1.TaskStatus_TASK_STATUS_FAILED
			req.ErrorMessage = taskErr.Error()
		}

		for attempt := 1; attempt <= 3; attempt++ {
			rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := client.ReportTaskResult(rpcCtx, req)
			cancel()
			if err == nil || status.Code(err) == codes.NotFound {
				return
			}
			slog.Warn("reporting task result failed", "task_id", t.ID, "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		slog.Error("giving up reporting task result", "task_id", t.ID)
	}
}
