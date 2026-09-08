package quota

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Warn at 90, refuse at 100, and UNCAPPED is always ok — a dimension with no
// cap cannot be exceeded, and drawing it amber would train the operator to
// ignore the colour.
func TestQuotaStateThresholds(t *testing.T) {
	limit := func(n int64) *int64 { return &n }
	cases := []struct {
		used  int64
		limit *int64
		want  string
	}{
		{0, nil, domain.QuotaOK},
		{1 << 40, nil, domain.QuotaOK},
		{0, limit(100), domain.QuotaOK},
		{89, limit(100), domain.QuotaOK},
		{90, limit(100), domain.QuotaWarn},
		{99, limit(100), domain.QuotaWarn},
		{100, limit(100), domain.QuotaExceeded},
		{101, limit(100), domain.QuotaExceeded},
	}
	for _, c := range cases {
		if got := domain.QuotaState(c.used, c.limit); got != c.want {
			t.Errorf("QuotaState(%d, %v) = %q, want %q", c.used, c.limit, got, c.want)
		}
	}
}

// A quota is denominated in bytes and counts. This test exists so a future
// change that adds a monetary field to the row has to delete an assertion that
// says why it must not — ADR-012 rule 1, made mechanical.
func TestNoMonetaryConceptExistsOnAQuota(t *testing.T) {
	forbidden := []string{"price", "cost", "rate", "currency", "plan", "tier", "invoice", "amount"}
	fields := reflectFieldNames(domain.ResourceQuota{})
	for _, f := range fields {
		lower := lowerASCII(f)
		for _, bad := range forbidden {
			if contains(lower, bad) {
				t.Errorf("ResourceQuota has a field %q, which looks monetary. ADR-012 permits quotas precisely because they are not: adding one is a change to the vision's out-of-scope list and needs its own recorded decision.", f)
			}
		}
	}
}

func reflectFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ─── The sum of a team's project caps (§3) ──────────────────────────────────
//
// Over-commitment is allowed on purpose, so the number exists to be SHOWN and
// never to be checked. What has to hold is that it is honest: the sum counts
// only projects that actually have a cap, and the projects that do not are
// counted separately — a sum with nothing behind it reads as "nothing
// promised" when it means "nothing bounded".

type stubStore struct {
	projects map[string][]string             // team -> projects
	quotas   map[string]domain.ResourceQuota // project -> quota
}

func (s stubStore) ListProjectsInTeam(_ context.Context, teamID string) ([]string, error) {
	return s.projects[teamID], nil
}

func (s stubStore) GetProjectQuota(_ context.Context, projectID string) (domain.ResourceQuota, error) {
	q, ok := s.quotas[projectID]
	if !ok {
		return domain.ResourceQuota{}, errNoQuota
	}
	return q, nil
}

var errNoQuota = errors.New("no quota")

// The rest of the Store, unused by Report's committed path.
func (stubStore) UpsertProjectQuota(context.Context, string, string, domain.ResourceQuota) (domain.ResourceQuota, error) {
	return domain.ResourceQuota{}, nil
}
func (stubStore) UpsertTeamQuota(context.Context, string, string, domain.ResourceQuota) (domain.ResourceQuota, error) {
	return domain.ResourceQuota{}, nil
}
func (s stubStore) GetTeamQuota(context.Context, string) (domain.ResourceQuota, error) {
	return domain.ResourceQuota{}, errNoQuota
}
func (stubStore) ListResourceQuotas(context.Context) ([]domain.ResourceQuota, error) { return nil, nil }
func (stubStore) DeleteProjectQuota(context.Context, string) error                   { return nil }
func (stubStore) DeleteTeamQuota(context.Context, string) error                      { return nil }
func (stubStore) ProjectDeclaredMemory(context.Context, string) (int64, error)       { return 0, nil }
func (stubStore) ProjectUnlimitedResources(context.Context, string) ([]string, error) {
	return nil, nil
}
func (stubStore) ProjectComposeStackCount(context.Context, string) (int, error) { return 0, nil }
func (stubStore) ProjectObservedDisk(context.Context, string) (int64, error)    { return 0, nil }
func (stubStore) ProjectLivePreviews(context.Context, string) (int, error)      { return 0, nil }
func (stubStore) GetProject(context.Context, string) (domain.Project, error) {
	return domain.Project{}, errNoQuota
}
func (stubStore) GetQuotaState(context.Context, string, string, string) (string, time.Time, error) {
	return "", time.Time{}, errNoQuota
}
func (stubStore) SetQuotaState(context.Context, string, string, string, string) error { return nil }

func mb(n int64) *int64 { v := n * 1024 * 1024; return &v }

func TestTeamReportSumsProjectCaps(t *testing.T) {
	store := stubStore{
		projects: map[string][]string{"tm_1": {"pr_a", "pr_b", "pr_c"}},
		quotas: map[string]domain.ResourceQuota{
			"pr_a": {MemoryLimitBytes: mb(512)},
			"pr_b": {MemoryLimitBytes: mb(1024)},
			// pr_c has no quota row at all — the ordinary case, and the one
			// that makes a bare sum misleading.
		},
	}
	s := New(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report, err := s.Report(context.Background(), domain.QuotaScopeTeam, "tm_1")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	memory := usageFor(t, report, domain.QuotaMemory)
	if memory.Committed == nil || *memory.Committed != 1536*1024*1024 {
		t.Errorf("committed memory = %v, want 1536 MiB", memory.Committed)
	}
	if memory.UncappedProjects == nil || *memory.UncappedProjects != 1 {
		t.Errorf("uncapped projects = %v, want 1", memory.UncappedProjects)
	}
	// Disk: nobody capped it, so the sum is zero and ALL THREE projects are
	// named as unbounded rather than the row reading as a tidy 0 B.
	disk := usageFor(t, report, domain.QuotaDisk)
	if disk.Committed == nil || *disk.Committed != 0 {
		t.Errorf("committed disk = %v, want 0", disk.Committed)
	}
	if disk.UncappedProjects == nil || *disk.UncappedProjects != 3 {
		t.Errorf("uncapped disk projects = %v, want 3", disk.UncappedProjects)
	}
}

// A PROJECT report carries no commitment at all: nothing sits below a project,
// and a "0 committed" there would answer a question nobody asked.
func TestProjectReportCarriesNoCommitment(t *testing.T) {
	s := New(stubStore{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report, err := s.Report(context.Background(), domain.QuotaScopeProject, "pr_a")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	for _, u := range report.Usage {
		if u.Committed != nil || u.UncappedProjects != nil {
			t.Errorf("%s: project report carries a commitment (%v, %v); absence is what makes it mean \"does not apply\"",
				u.Dimension, u.Committed, u.UncappedProjects)
		}
	}
}

func usageFor(t *testing.T, r domain.QuotaReport, dimension string) domain.QuotaUsage {
	t.Helper()
	for _, u := range r.Usage {
		if u.Dimension == dimension {
			return u
		}
	}
	t.Fatalf("no %q row in the report", dimension)
	return domain.QuotaUsage{}
}
