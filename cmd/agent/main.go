// cypher-agent is the per-server CypherPanel daemon. It detects the distro
// layout, connects to CypherCore over mTLS gRPC, registers itself, and keeps
// a heartbeat. Target: <50MB idle RSS, no per-hosted-account processes
// (plan.md Section 8).
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/config"
	"github.com/MaramHarsha/CypherPanel/internal/hoststats"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/paths"
	"github.com/MaramHarsha/CypherPanel/internal/pki"
	"github.com/MaramHarsha/CypherPanel/internal/platform"
	"github.com/MaramHarsha/CypherPanel/internal/services"
	"github.com/MaramHarsha/CypherPanel/internal/webserver"
)

// version is stamped via -ldflags at release time.
var version = "dev"

const heartbeatInterval = 30 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("cypher-agent failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAgent()
	if err != nil {
		return err
	}

	family, layout := paths.Detect()
	slog.Info("cypher-agent starting",
		"version", version,
		"env", cfg.Env,
		"core_addr", cfg.CoreAddr,
		"distro_family", string(family),
		"nginx_conf_dir", layout.NginxConfDir,
	)
	if family == paths.FamilyUnknown && cfg.Env == config.EnvProduction {
		slog.Warn("unrecognized distro family; using generic path layout — set CYPHER_PATH_* overrides if needed")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := dialCore(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := agentv1.NewAgentServiceClient(conn)

	serverID, err := register(ctx, client, string(family))
	if err != nil {
		return err
	}
	slog.Info("registered with control plane", "server_id", serverID)

	// Task consumer: pull provisioning jobs for this server from JetStream.
	natsOpts := []nats.Option{nats.Name("cypher-agent")}
	if cfg.NATSCreds != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(cfg.NATSCreds))
	}
	nc, err := nats.Connect(cfg.NATSURL, natsOpts...)
	if err != nil {
		return err
	}
	defer nc.Drain()
	executor := &taskExecutor{
		layout: layout,
		users:  platform.New(),
		sites:  platform.NewSites(),
		vhost:  webserver.Nginx{},
	}
	consumerErr := make(chan error, 1)
	go func() {
		consumerErr <- jobs.Consume(ctx, nc, serverID, executor.Handle, reportResult(client, serverID))
	}()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("cypher-agent shutting down")
			return nil
		case err := <-consumerErr:
			return err
		case <-ticker.C:
			stats := hoststats.Sample()
			var svcs []*agentv1.ServiceStatus
			for _, s := range services.Sample() {
				svcs = append(svcs, &agentv1.ServiceStatus{Name: s.Name, State: s.State})
			}
			hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := client.Heartbeat(hbCtx, &agentv1.HeartbeatRequest{
				ServerId: serverID,
				Stats: &agentv1.HostStats{
					Load_1M:          stats.Load1m,
					MemoryTotalBytes: stats.MemoryTotalBytes,
					MemoryUsedBytes:  stats.MemoryUsedBytes,
					DiskTotalBytes:   stats.DiskTotalBytes,
					DiskUsedBytes:    stats.DiskUsedBytes,
				},
				Services: svcs,
			})
			cancel()
			switch {
			case err == nil:
			case status.Code(err) == codes.NotFound:
				// Control plane no longer knows us (row deleted): re-enroll.
				slog.Warn("server unknown to control plane; re-registering")
				if serverID, err = register(ctx, client, string(family)); err != nil {
					slog.Error("re-registration failed; will retry on next heartbeat", "error", err)
				}
			default:
				slog.Warn("heartbeat failed; will retry", "error", err)
			}
		}
	}
}

func dialCore(cfg config.Agent) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if cfg.TLSCertFile != "" {
		tlsCfg, err := pki.ClientTLS(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile)
		if err != nil {
			return nil, err
		}
		creds = credentials.NewTLS(tlsCfg)
	} else {
		// config.LoadAgent forbids this in production.
		slog.Warn("dialing core WITHOUT mTLS — development only")
		creds = insecure.NewCredentials()
	}
	return grpc.NewClient(cfg.CoreAddr, grpc.WithTransportCredentials(creds))
}

func register(ctx context.Context, client agentv1.AgentServiceClient, distroFamily string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}

	// Register with retry/backoff: on boot the control plane may not be
	// reachable yet, and giving up would leave the server unmanaged.
	backoff := 2 * time.Second
	for {
		regCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		resp, err := client.Register(regCtx, &agentv1.RegisterRequest{
			Hostname:     hostname,
			IpAddress:    outboundIP(),
			AgentVersion: version,
			DistroFamily: distroFamily,
		})
		cancel()
		if err == nil {
			return resp.GetServerId(), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		slog.Warn("register failed; retrying", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// outboundIP returns this host's primary outbound IP (no packets are sent —
// the UDP "connection" only resolves the local address for the route).
func outboundIP() string {
	conn, err := net.Dial("udp", "203.0.113.1:9") // TEST-NET-3 documentation address
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "127.0.0.1"
	}
	return addr.IP.String()
}
