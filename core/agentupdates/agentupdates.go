// Package agentupdates owns the plane's half of ADR-010: two release channels,
// which channel each server follows, and the gate between them
// (docs/features/agent-updates.md).
//
// The plane names a version and never stores or serves a byte of the binary.
// It does not distribute, does not sign, and does not verify — the agent
// fetches a signed artifact itself and runs nothing a baked-in key does not
// verify. What lives here is the DECISION: which version, for which hosts, and
// whether promoting canary to stable is allowed yet.
package agentupdates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Errors a handler maps to a status code (ENGINEERING rule 3).
var (
	// ErrUnknownChannel: not `stable` or `canary`.
	ErrUnknownChannel = errors.New("agentupdates: unknown release channel")
	// ErrNewerThanPanel: the version asked for is newer than the panel's own
	// build. Additive-only proto guarantees the OLD-agent direction, not the
	// new-agent one, so the refusal names the remedy: update the panel first.
	ErrNewerThanPanel = errors.New("agentupdates: that agent version is newer than this panel")
	// ErrArtifactUnreachable: the pre-flight HEAD could not confirm the
	// manifest exists. A typo is refused where it is cheap rather than
	// discovered by forty hosts.
	ErrArtifactUnreachable = errors.New("agentupdates: the release manifest could not be reached")
	// ErrPrivateArtifact: the artifact base resolves inside the panel's own
	// network. This is the plane connecting to a host named in a request body —
	// threat-model §5.14's registry probe exactly — so it takes §5.14's
	// controls. CYPHERD_AGENT_UPDATE_PRECHECK=off is how an operator says "the
	// agents can reach it and you cannot", rather than the plane relaxing a
	// control because a request body asked it to.
	ErrPrivateArtifact = errors.New("agentupdates: refusing to probe an address inside the panel's own network")
	// ErrGateNoConverged / ErrGateRolledBack are the two canary refusals (§5).
	ErrGateNoConverged = errors.New("agentupdates: no canary server has converged on that version")
	ErrGateRolledBack  = errors.New("agentupdates: a canary server rolled that version back")
)

// Store is the persistence this needs (consumer-defined; *store.Store
// satisfies it).
type Store interface {
	ListAgentChannels(ctx context.Context) ([]domain.AgentChannelRow, error)
	GetAgentChannel(ctx context.Context, channel string) (domain.AgentChannelRow, error)
	SetAgentChannel(ctx context.Context, channel, version, artifactBase string, rollback bool, by string) (domain.AgentChannelRow, error)
	SetServerAgentChannel(ctx context.Context, id, channel string) (domain.Server, error)
	ListServers(ctx context.Context) ([]domain.Server, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
}

// Resyncer asks agents to re-read desired state. A version change propagates on
// the existing resync nudge, whose contract is already "re-read your desired
// state" — so no new subject and no new work kind (agent-updates.md §7).
type Resyncer interface {
	RequestResync(ctx context.Context, reason string) error
	RequestServerResync(ctx context.Context, serverID, reason string) error
}

// Service is the plane-side channel owner.
type Service struct {
	store    Store
	resync   Resyncer
	panelVer string
	// precheck HEADs the manifest when a version is set. Off for a source only
	// agents can reach.
	precheck bool
	client   *http.Client
	resolver interface {
		LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	}
	log *slog.Logger
}

// New wires the service. panelVersion is the plane's own build, which is the
// ceiling on what may be asked of an agent.
func New(s Store, r Resyncer, panelVersion string, precheck bool, log *slog.Logger) *Service {
	return &Service{
		store: s, resync: r, panelVer: panelVersion, precheck: precheck,
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("agentupdates: too many redirects")
				}
				return nil
			},
		},
		resolver: net.DefaultResolver,
		log:      log,
	}
}

// View is what the screen reads: both channels, the fleet, and the resolved
// artifact base per channel.
type View struct {
	PanelVersion string
	Channels     []domain.AgentChannelRow
	Servers      []ServerView
	// Histogram is running version → count, for the empty state's one line.
	Histogram map[string]int
}

// ServerView is one row of the fleet table.
type ServerView struct {
	Server domain.Server
	// Desired is the version this server's channel names, resolved here so the
	// screen never has to join two lists to answer "is it converged".
	Desired string
	// Converged is true when the running version equals the desired one. An
	// empty desired version is converged by definition: no instruction is not a
	// pending update.
	Converged bool
}

// Get assembles the view.
func (s *Service) Get(ctx context.Context) (View, error) {
	channels, err := s.store.ListAgentChannels(ctx)
	if err != nil {
		return View{}, err
	}
	byChannel := make(map[string]domain.AgentChannelRow, len(channels))
	for _, c := range channels {
		byChannel[c.Channel] = c
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return View{}, err
	}
	out := View{PanelVersion: s.panelVer, Channels: channels, Histogram: map[string]int{}}
	for _, srv := range servers {
		desired := byChannel[channelOf(srv)].DesiredVersion
		out.Servers = append(out.Servers, ServerView{
			Server:    srv,
			Desired:   desired,
			Converged: desired == "" || desired == srv.AgentVersion,
		})
		if srv.AgentVersion != "" {
			out.Histogram[srv.AgentVersion]++
		}
	}
	sort.Slice(out.Servers, func(i, j int) bool { return out.Servers[i].Server.Name < out.Servers[j].Server.Name })
	return out, nil
}

// SpecFor resolves one server's desired agent version — what the scheduler puts
// on the wire. An empty version means NO INSTRUCTION, never "downgrade to
// nothing".
func (s *Service) SpecFor(ctx context.Context, srv domain.Server) (version, artifactBase string, rollback bool, err error) {
	row, err := s.store.GetAgentChannel(ctx, channelOf(srv))
	if err != nil {
		return "", "", false, err
	}
	return row.DesiredVersion, row.ArtifactBase, row.Rollback, nil
}

// Set writes one channel's desired version. An empty version clears the
// instruction, which is how an operator stops a rollout they no longer want.
//
// The rollback flag is DERIVED rather than asked for: setting an older version
// than the one already desired sets it, and moving forward clears it. An
// operator who has to remember a second checkbox to make a rollback work is an
// operator whose rollback does not happen.
func (s *Service) Set(ctx context.Context, channel, version, artifactBase, by string) (domain.AgentChannelRow, error) {
	if !domain.ValidChannel(channel) {
		return domain.AgentChannelRow{}, ErrUnknownChannel
	}
	version = strings.TrimSpace(version)
	artifactBase = strings.TrimSpace(artifactBase)
	if version == "" {
		return s.store.SetAgentChannel(ctx, channel, "", "", false, by)
	}
	if newerThanPanel(version, s.panelVer) {
		return domain.AgentChannelRow{}, fmt.Errorf("%w (%s): update the panel first", ErrNewerThanPanel, s.panelVer)
	}
	current, err := s.store.GetAgentChannel(ctx, channel)
	if err != nil {
		return domain.AgentChannelRow{}, err
	}
	rollback := goesBackwards(current.DesiredVersion, version)
	if err := s.preflight(ctx, version, artifactBase); err != nil {
		return domain.AgentChannelRow{}, err
	}
	row, err := s.store.SetAgentChannel(ctx, channel, version, artifactBase, rollback, by)
	if err != nil {
		return domain.AgentChannelRow{}, err
	}
	s.nudge(ctx, "agent-channel-"+channel)
	return row, nil
}

// Promote makes stable's desired version canary's — one button, one write.
//
// It is refused when no canary server has CONVERGED on the candidate (promoting
// a version no host has run defeats a gate whose whole purpose is that a host
// ran it) or when any canary server reports it rolled back. There is no
// override flag: an operator who believes a rollback was spurious sets stable's
// version directly and owns that explicitly.
func (s *Service) Promote(ctx context.Context, by string) (domain.AgentChannelRow, error) {
	canary, err := s.store.GetAgentChannel(ctx, domain.ChannelCanary)
	if err != nil {
		return domain.AgentChannelRow{}, err
	}
	if canary.DesiredVersion == "" {
		return domain.AgentChannelRow{}, fmt.Errorf("%w: canary names no version", ErrGateNoConverged)
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return domain.AgentChannelRow{}, err
	}
	converged, rolledBack := 0, []string(nil)
	running := map[string]int{}
	for _, srv := range servers {
		if channelOf(srv) != domain.ChannelCanary {
			continue
		}
		// A canary server that is merely OFFLINE neither blocks nor counts.
		// Blocking would make one powered-down host a permanent hold on every
		// fleet update, and an operator who learns the gate refuses for reasons
		// unrelated to the release is an operator who stops reading refusals.
		if srv.Status == domain.StatusUnknown {
			continue
		}
		if srv.AgentUpdatePhase == domain.AgentPhaseRolledBack && srv.AgentUpdateTarget == canary.DesiredVersion {
			rolledBack = append(rolledBack, srv.Name)
			continue
		}
		if srv.AgentVersion == canary.DesiredVersion {
			converged++
		}
		if srv.AgentVersion != "" {
			running[srv.AgentVersion]++
		}
	}
	if len(rolledBack) > 0 {
		return domain.AgentChannelRow{}, fmt.Errorf("%w: %s", ErrGateRolledBack, strings.Join(rolledBack, ", "))
	}
	if converged == 0 {
		return domain.AgentChannelRow{}, fmt.Errorf("%w: canary is running %s", ErrGateNoConverged, describe(running))
	}
	row, err := s.store.SetAgentChannel(ctx, domain.ChannelStable,
		canary.DesiredVersion, canary.ArtifactBase, false, by)
	if err != nil {
		return domain.AgentChannelRow{}, err
	}
	s.nudge(ctx, "agent-channel-promote")
	return row, nil
}

// SetServerChannel moves one server between channels and nudges only that
// server: a channel change is one host's business, and waking the fleet for it
// would make every dropdown a fleet-wide event.
func (s *Service) SetServerChannel(ctx context.Context, serverID, channel string) (domain.Server, error) {
	if !domain.ValidChannel(channel) {
		return domain.Server{}, ErrUnknownChannel
	}
	srv, err := s.store.SetServerAgentChannel(ctx, serverID, channel)
	if err != nil {
		return domain.Server{}, err
	}
	if s.resync != nil {
		if err := s.resync.RequestServerResync(ctx, serverID, "agent-channel"); err != nil {
			s.log.Warn("nudging a server after a channel change", "server_id", serverID, "error", err)
		}
	}
	return srv, nil
}

// nudge asks the fleet to re-read desired state. Failure is logged and
// swallowed: the channel is already persisted, so the worst case is that it
// applies at the next sync rather than now (ENGINEERING rule 15).
func (s *Service) nudge(ctx context.Context, reason string) {
	if s.resync == nil {
		return
	}
	if err := s.resync.RequestResync(ctx, reason); err != nil {
		s.log.Warn("nudging the fleet after an agent channel change", "reason", reason, "error", err)
	}
}

// preflight HEADs the manifest so a typo is refused where it is cheap.
func (s *Service) preflight(ctx context.Context, version, artifactBase string) error {
	if !s.precheck {
		return nil
	}
	base := artifactBase
	if base == "" {
		base = DefaultArtifactBase(version)
	}
	u, err := url.Parse(strings.TrimSuffix(base, "/") + "/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("%w: %s is not a URL", ErrArtifactUnreachable, base)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s scheme refused", ErrArtifactUnreachable, u.Scheme)
	}
	if err := s.checkPublic(ctx, u.Hostname()); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactUnreachable, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrArtifactUnreachable, u.Redacted())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: %s answered %d", ErrArtifactUnreachable, u.Redacted(), resp.StatusCode)
	}
	return nil
}

// checkPublic refuses a host that resolves inside the panel's own network. A
// private mirror is precisely what this must refuse, which is what the off
// switch is for.
func (s *Service) checkPublic(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("%w: no host", ErrArtifactUnreachable)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return refuseInternal(addr)
	}
	ips, err := s.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: %s does not resolve", ErrArtifactUnreachable, host)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			continue
		}
		if err := refuseInternal(addr.Unmap()); err != nil {
			return err
		}
	}
	return nil
}

func refuseInternal(addr netip.Addr) error {
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() {
		return ErrPrivateArtifact
	}
	return nil
}

// DefaultArtifactBase is where this project publishes a release's assets. It is
// exported so the screen can show what an empty artifact base resolves to
// rather than showing a blank field.
func DefaultArtifactBase(version string) string {
	return "https://github.com/MaramHarsha/CypherPanel/releases/download/" + version
}

func channelOf(s domain.Server) string {
	if domain.ValidChannel(s.AgentChannel) {
		return s.AgentChannel
	}
	return domain.ChannelStable
}

func describe(running map[string]int) string {
	if len(running) == 0 {
		return "nothing (no canary server has been heard from)"
	}
	parts := make([]string, 0, len(running))
	for v, n := range running {
		parts = append(parts, fmt.Sprintf("%s on %d", v, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
