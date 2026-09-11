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
	"net/http"
	"net/url"
	"regexp"
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
	// ErrBadArtifactBase: the artifact base is not a well-formed absolute
	// http(s) URL. Shape only — a mirror may live anywhere the AGENTS can reach,
	// including inside a private network, and the plane never connects to it.
	ErrBadArtifactBase = errors.New("agentupdates: the artifact base must be an absolute http(s) URL with no credentials, query or fragment")
	// ErrBadVersion: the version is not tag-shaped. It is bounded because it is
	// concatenated into a URL — the plane's own pre-flight, and every agent's
	// download — and `../../` in a tag would aim a fetcher at an arbitrary path
	// under the release host. The same bound core/upgrade.ValidTag applies to
	// the panel's own releases, for the same reason.
	ErrBadVersion = errors.New("agentupdates: that does not look like a release tag")
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
	// precheck HEADs the manifest when a version is set — but ONLY against this
	// project's own release host, never against an operator-named mirror. See
	// preflight for why that is structural rather than a setting.
	precheck bool
	client   *http.Client
	log      *slog.Logger
}

// New wires the service. panelVersion is the plane's own build, which is the
// ceiling on what may be asked of an agent.
func New(s Store, r Resyncer, panelVersion string, precheck bool, log *slog.Logger) *Service {
	return &Service{
		store: s, resync: r, panelVer: panelVersion, precheck: precheck,
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("agentupdates: too many redirects")
				}
				return nil
			},
		},
		log: log,
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
	// Shape first, and before anything is stored: this string is concatenated
	// into a URL by the plane's pre-flight and by every agent's download, so a
	// tag carrying `../` or a scheme would be a path the panel never named.
	if !ValidTag(version) {
		return domain.AgentChannelRow{}, fmt.Errorf("%w: %q", ErrBadVersion, version)
	}
	if artifactBase != "" {
		if err := validArtifactBase(artifactBase); err != nil {
			return domain.AgentChannelRow{}, err
		}
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

// preflight HEADs the release manifest so a typo is refused where it is cheap
// rather than discovered by forty hosts.
//
// It probes ONE host: the compile-time constant this project publishes releases
// from. An operator-supplied artifact_base is never fetched by the plane, and
// that is structural rather than a setting — the earlier version of this
// function took the base from the request body and probed it behind a
// private-address check, which is a server-side request forgery primitive with
// a guard on it (CWE-918). Two things were wrong with the guard and one thing
// was wrong with the shape:
//
//   - the redirect hook counted hops but did not re-check the target, so a
//     public host could 302 the probe onto 169.254.169.254;
//   - the check resolved the name, and then the transport resolved it AGAIN, so
//     a name that answers differently the second time walked through;
//   - and no amount of guarding makes "connect to the host in this request
//     body" not be that. A 200 against a refusal against a timeout separates a
//     listening port from a closed one from a filtered one inside the panel's
//     network, which is exactly what threat-model §5.14 is about.
//
// So the input selects a path under a known fixed host instead. A mirror is
// validated for SHAPE and left alone — which loses nothing real, because a
// mirror is by definition somewhere only the agents can reach, and probing it
// was what CYPHERD_AGENT_UPDATE_PRECHECK=off already existed to stop. The
// typo this catches is a mistyped TAG, which is the mistake operators actually
// make and which lives on the default path.
func (s *Service) preflight(ctx context.Context, version, artifactBase string) error {
	if !s.precheck || artifactBase != "" {
		return nil
	}
	if !ValidTag(version) {
		return fmt.Errorf("%w: %q", ErrBadVersion, version)
	}
	// Built from a constant host and a tag that has passed ValidTag, so nothing
	// an operator typed can move this off releaseHost.
	target := DefaultArtifactBase(version) + "/SHA256SUMS"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactUnreachable, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrArtifactUnreachable, target)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: %s answered %d — is %s a published release?",
			ErrArtifactUnreachable, target, resp.StatusCode, version)
	}
	return nil
}

// validArtifactBase bounds a mirror's SHAPE, which is all the plane can honestly
// say about an address it will never connect to. Credentials, a query and a
// fragment are shapes a release prefix never has, and refusing them removes
// three ways to smuggle something past whoever reads the field back.
func validArtifactBase(base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrBadArtifactBase, base)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: %q", ErrBadArtifactBase, base)
	}
	if strings.Contains(u.Path, "..") {
		return fmt.Errorf("%w: %q", ErrBadArtifactBase, base)
	}
	return nil
}

// releaseHost is where this project publishes its releases. It is a constant so
// that the plane's pre-flight has exactly one destination, chosen at compile
// time — the "known fixed string" half of the remedy for CWE-918.
const releaseHost = "https://github.com/MaramHarsha/CypherPanel/releases/download/"

// DefaultArtifactBase is where this project publishes a release's assets. It is
// exported so the screen can show what an empty artifact base resolves to
// rather than showing a blank field.
//
// Callers that build a URL from it must pass a version that has cleared
// ValidTag; the two are used together everywhere in this package.
func DefaultArtifactBase(version string) string { return releaseHost + version }

// tagShape is what a release tag may look like. Bounded rather than merely
// non-empty because this string is concatenated into URLs on both sides of the
// wire: a tag of "../../evil" would aim a fetcher at an arbitrary path under
// the release host, which is precisely the defect core/upgrade.ValidTag exists
// to prevent for the panel's own releases.
var tagShape = regexp.MustCompile(`^v?[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)

// ValidTag reports whether s names a release. Exported because the same
// judgement is made when a version is SET and again when it is probed, and two
// copies of it could disagree.
func ValidTag(s string) bool { return len(s) <= 48 && tagShape.MatchString(s) }

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
