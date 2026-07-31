package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/acme"
	"github.com/MaramHarsha/CypherPanel/internal/backups"
	"github.com/MaramHarsha/CypherPanel/internal/dkim"
	"github.com/MaramHarsha/CypherPanel/internal/ftp"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/mailstore"
	"github.com/MaramHarsha/CypherPanel/internal/paths"
	"github.com/MaramHarsha/CypherPanel/internal/phpruntime"
	"github.com/MaramHarsha/CypherPanel/internal/platform"
	"github.com/MaramHarsha/CypherPanel/internal/services"
	"github.com/MaramHarsha/CypherPanel/internal/usersdb"
	"github.com/MaramHarsha/CypherPanel/internal/webserver"
)

// taskExecutor dispatches queued tasks to their handlers. Every handler must
// be idempotent — JetStream may deliver the same task more than once.
type taskExecutor struct {
	layout  paths.Layout
	family  paths.Family
	users   platform.SystemUsers
	sites   platform.Sites
	vhost   webserver.VHostRenderer
	acme    *acme.Issuer
	usersDB usersdb.Manager // nil when no user-DB backend is configured
	ftp     ftp.Manager
	mail    mailstore.Manager // nil when no mail backend is configured
	backups backups.Engine   // nil when no backup engine (restic) is installed
	dumper  usersdb.Dumper   // nil when no user-DB backend is configured
	// core + serverID let backup tasks fetch destination credentials over the
	// authenticated mTLS channel instead of carrying them in task payloads.
	core     agentv1.AgentServiceClient
	serverID string
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

	case jobs.TypePHPVersionChange:
		return nil, e.changePHPVersion(ctx, t.Payload)

	case jobs.TypeSSLIssue:
		return e.issueSSL(ctx, t.Payload)

	case jobs.TypeServiceControl:
		return nil, e.controlService(ctx, t.Payload)

	case jobs.TypePHPRuntime:
		return nil, e.phpRuntime(ctx, t.Payload)

	case jobs.TypeDBCreate:
		return e.createDB(ctx, t.Payload)

	case jobs.TypeDBDrop:
		return nil, e.dropDB(ctx, t.Payload)

	case jobs.TypeFTPCreate:
		return e.createFTP(ctx, t.Payload)

	case jobs.TypeFTPDelete:
		return nil, e.deleteFTP(ctx, t.Payload)

	case jobs.TypeMailCreate:
		return e.createMail(ctx, t.Payload)

	case jobs.TypeMailDelete:
		return nil, e.deleteMail(ctx, t.Payload)

	case jobs.TypeBackupRun:
		return e.runBackup(ctx, t.ID, t.Payload)

	case jobs.TypeBackupRestore:
		return e.runRestore(ctx, t.ID, t.Payload)

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
	return e.applySite(ctx, p.Username, p.Domain, p.PHPVersion, p.MemoryMB, p.PHPSettings)
}

// changePHPVersion moves an account from one PHP branch to another. Because the
// account's FPM socket is version-independent, the old version's pool must be
// removed (releasing the socket) before the new version's pool is written, or
// two FPM masters would contend for the same socket.
func (e *taskExecutor) changePHPVersion(ctx context.Context, raw []byte) error {
	var p jobs.PHPVersionChangePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if p.Username == "" || p.Domain == "" || p.NewPHPVersion == "" {
		return jobs.Permanent(errors.New("username, domain and new_php_version are required"))
	}

	// Release the socket from the old pool first (idempotent; skips when the
	// version is unchanged or the old pool is already gone).
	if p.OldPHPVersion != "" && p.OldPHPVersion != p.NewPHPVersion {
		oldPool := e.layout.PHPFPMPoolPath(p.OldPHPVersion, p.Username)
		if err := e.sites.RemovePHPPool(ctx, oldPool, p.OldPHPVersion); err != nil {
			if errors.Is(err, platform.ErrUnsupported) {
				return jobs.Permanent(err)
			}
			return err
		}
	}
	return e.applySite(ctx, p.Username, p.Domain, p.NewPHPVersion, p.MemoryMB, p.PHPSettings)
}

// applySite renders and applies an account's vhost + PHP-FPM pool for a given
// PHP version. Shared by first provisioning, INI changes, and version changes,
// so all three converge on the same desired state. It is TLS-aware: if a
// certificate is already installed for the domain, the regenerated vhost keeps
// HTTPS instead of silently dropping the site back to plain HTTP.
func (e *taskExecutor) applySite(ctx context.Context, username, domain, phpVersion string, memoryMB int, phpSettings map[string]string) error {
	webRoot := e.layout.AccountWebRoot(username)
	logDir := e.layout.AccountLogDir(username)
	socket := e.layout.PHPFPMSocketPath(username)

	spec := webserver.VHostSpec{
		Domain:    domain,
		WebRoot:   webRoot,
		PHPSocket: socket,
		AccessLog: filepath.Join(logDir, domain+".access.log"),
		ErrorLog:  filepath.Join(logDir, domain+".error.log"),
	}
	// Preserve HTTPS across re-provisioning: a present, parseable cert means the
	// site is already on TLS and the vhost must stay on TLS.
	certPath := e.layout.SSLCertPath(domain)
	if !acme.CertValidUntil(certPath).IsZero() {
		spec.TLSCertPath = certPath
		spec.TLSKeyPath = e.layout.SSLKeyPath(domain)
	}
	vhostCfg, err := e.vhost.Render(spec)
	if err != nil {
		return jobs.Permanent(err)
	}

	// Package memory limit is the baseline; per-account INI overrides (already
	// allowlist-validated by Core) win where set.
	admin := map[string]string{}
	if memoryMB > 0 {
		admin["memory_limit"] = fmt.Sprintf("%dM", memoryMB)
	}
	for k, v := range phpSettings {
		admin[k] = v
	}
	poolCfg, err := webserver.RenderPHPFPMPool(webserver.PoolSpec{
		User:          username,
		Socket:        socket,
		WebServerUser: e.layout.WebServerUser,
		AdminValues:   admin,
	})
	if err != nil {
		return jobs.Permanent(err)
	}

	err = e.sites.Provision(ctx, platform.SiteSpec{
		Username:    username,
		AccountDirs: []string{webRoot, logDir},
		VHostPath:   e.layout.VhostConfPath(domain),
		VHostConfig: vhostCfg,
		PoolPath:    e.layout.PHPFPMPoolPath(phpVersion, username),
		PoolConfig:  poolCfg,
		PHPVersion:  phpVersion,
	})
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

// phpRuntime installs or removes a PHP-FPM branch via the distro package
// manager. Version + action are re-validated here; an unknown distro family or
// non-Linux platform is a permanent failure (nothing to retry).
func (e *taskExecutor) phpRuntime(ctx context.Context, raw []byte) error {
	var p jobs.PHPRuntimePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if !phpruntime.ValidVersion(p.Version) {
		return jobs.Permanent(fmt.Errorf("invalid php version %q", p.Version))
	}
	if !phpruntime.ValidAction(p.Action) {
		return jobs.Permanent(fmt.Errorf("invalid action %q", p.Action))
	}
	err := phpruntime.Run(ctx, e.family, p.Version, p.Action)
	if errors.Is(err, phpruntime.ErrUnsupported) {
		return jobs.Permanent(err)
	}
	return err // a package-manager failure (network, repo) is retryable
}

// createMail provisions a virtual mailbox: it upserts the auth-DB row (address
// → bcrypt hash / maildir / quota), creates the Maildir on disk, and ensures
// the domain has a DKIM signing key. The password never appears here — Core
// sends only the bcrypt hash.
//
// The DKIM public key is returned as result metadata so Core can publish the
// `<selector>._domainkey` TXT record; the private half never leaves this host.
func (e *taskExecutor) createMail(ctx context.Context, raw []byte) (map[string]string, error) {
	var p jobs.MailCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if e.mail == nil {
		return nil, jobs.Permanent(mailstore.ErrUnsupported)
	}
	if err := e.mail.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	if err := e.mail.UpsertMailbox(ctx, mailstore.Mailbox{
		Address: p.Address, Domain: p.Domain, Maildir: p.Maildir,
		PasswordHash: p.PasswordHash, QuotaBytes: int64(p.QuotaMB) << 20,
	}); err != nil {
		return nil, err
	}
	// Create the Maildir (cur/new/tmp) so the MTA can deliver immediately.
	base := e.layout.MaildirPath(p.Maildir)
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o700); err != nil {
			return nil, fmt.Errorf("creating maildir: %w", err)
		}
	}

	// DKIM is idempotent per domain: an existing key is reused, so a
	// redelivered task never rotates a key out from under working senders.
	meta := map[string]string{}
	key, err := dkim.EnsureKey(e.layout.DKIMDir, p.Domain, dkim.DefaultSelector)
	if err != nil {
		// The mailbox itself is provisioned and usable; failing the whole task
		// would retry the mailbox work too. Surface it in the log and let the
		// operator fix signing separately.
		slog.Error("provisioning DKIM key", "domain", p.Domain, "error", err)
		return meta, nil
	}
	if err := dkim.WriteRspamdConfig(e.layout.RspamdLocalDir, e.layout.DKIMDir, key.Selector); err != nil {
		slog.Warn("writing rspamd DKIM config", "error", err)
	} else if rerr := services.Control(ctx, "rspamd", "reload"); rerr != nil {
		// Not fatal: the key and config are on disk, so the next rspamd
		// restart picks them up.
		slog.Warn("reloading rspamd after DKIM config change", "error", rerr)
	}
	meta[jobs.MetaDKIMPublicTXT] = key.PublicTXT
	meta[jobs.MetaDKIMSelector] = key.Selector
	return meta, nil
}

// deleteMail removes the mailbox row and its Maildir (idempotent).
func (e *taskExecutor) deleteMail(ctx context.Context, raw []byte) error {
	var p jobs.MailDeletePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if e.mail == nil {
		return jobs.Permanent(mailstore.ErrUnsupported)
	}
	if err := e.mail.DeleteMailbox(ctx, p.Address); err != nil {
		return err
	}
	_ = os.RemoveAll(e.layout.MaildirPath(p.Maildir))
	return nil
}

// createFTP provisions a Pure-FTPd virtual user. Like db.create, the password
// is generated here (never in the payload) and returned as result metadata for
// Core to encrypt-and-store.
func (e *taskExecutor) createFTP(ctx context.Context, raw []byte) (map[string]string, error) {
	var p jobs.FTPCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	home := e.layout.AccountHome(p.SystemUser) // distro-aware, agent-derived
	password, err := ftp.GeneratePassword()
	if err != nil {
		return nil, err
	}
	err = e.ftp.Provision(ctx, ftp.Spec{
		Username: p.Username, SystemUser: p.SystemUser, HomeDir: home, Password: password,
	})
	if errors.Is(err, ftp.ErrUnsupported) {
		return nil, jobs.Permanent(err)
	}
	if err != nil {
		return nil, err
	}
	return map[string]string{jobs.MetaFTPPassword: password, jobs.MetaFTPHome: home}, nil
}

// deleteFTP removes a Pure-FTPd virtual user (idempotent).
func (e *taskExecutor) deleteFTP(ctx context.Context, raw []byte) error {
	var p jobs.FTPDeletePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	err := e.ftp.Deprovision(ctx, p.Username)
	if errors.Is(err, ftp.ErrUnsupported) {
		return jobs.Permanent(err)
	}
	return err
}

// createDB provisions a hosted-account database + user. The password is
// GENERATED here (never carried in the task payload / stream) and returned as
// result metadata so Core can encrypt-and-store it and show it to the user once.
func (e *taskExecutor) createDB(ctx context.Context, raw []byte) (map[string]string, error) {
	var p jobs.DBCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if e.usersDB == nil {
		return nil, jobs.Permanent(usersdb.ErrUnsupported)
	}
	password, err := usersdb.GeneratePassword()
	if err != nil {
		return nil, err
	}
	if err := e.usersDB.Provision(ctx, usersdb.Spec{
		Database: p.Name, User: p.DBUser, Host: p.DBHost, Password: password,
	}); err != nil {
		return nil, err // DB connectivity issues are retryable
	}
	return map[string]string{jobs.MetaDBPassword: password}, nil
}

// dropDB removes a hosted-account database and its user (idempotent).
func (e *taskExecutor) dropDB(ctx context.Context, raw []byte) error {
	var p jobs.DBDropPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if e.usersDB == nil {
		return jobs.Permanent(usersdb.ErrUnsupported)
	}
	return e.usersDB.Deprovision(ctx, usersdb.Spec{Database: p.Name, User: p.DBUser, Host: p.DBHost})
}

// controlService runs a lifecycle action on a managed system service. The
// service and action are re-validated here (never trust the payload) so a
// task can only ever target an allowlisted unit with a known verb.
func (e *taskExecutor) controlService(ctx context.Context, raw []byte) error {
	var p jobs.ServiceControlPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	if !services.ValidAction(p.Action) {
		return jobs.Permanent(fmt.Errorf("unsupported service action %q", p.Action))
	}
	if !services.IsManaged(p.Service) {
		return jobs.Permanent(fmt.Errorf("service %q is not managed", p.Service))
	}
	err := services.Control(ctx, p.Service, p.Action)
	if errors.Is(err, services.ErrUnsupported) {
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
	if errors.Is(err, acme.ErrWildcardNeedsDNS) {
		return nil, jobs.Permanent(err) // config error, not transient
	}
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
