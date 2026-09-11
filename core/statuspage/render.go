package statuspage

// The public payload (status-pages.md §2, §7).
//
// THE STRUCTURAL PROMISE. Everything a visitor can see is in the types below,
// and they are their own types — not narrowed copies of a DTO the panel
// already returns. There is no resource id in them, no server field, no route
// domain, no revision, no image reference and no `status_detail` to forget to
// omit. Reusing an internal DTO and deleting fields is how the third field
// added next year becomes public by accident; a separate type makes the leak a
// compile-time addition somebody has to write on purpose.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// WindowDays is how much history the page shows. Retention keeps longer (§11)
// so a wider window is possible later with no hole in the data.
const WindowDays = 30

// PublicPage is the whole document, in one struct.
type PublicPage struct {
	Title      string            `json:"title"`
	Domain     string            `json:"domain"`
	State      string            `json:"state"`
	Headline   string            `json:"headline"`
	Components []PublicComponent `json:"components"`
	Incidents  []PublicIncident  `json:"incidents"`
	UpdatedAt  time.Time         `json:"updated_at"`
	WindowDays int               `json:"window_days"`
}

type PublicComponent struct {
	Label string `json:"label"`
	State string `json:"state"`
	// Uptime is nil when nothing was observed in the window. A page that has
	// measured nothing prints "—", never "100%" — printing a perfect score for
	// an unmeasured service is the most common dishonesty in this product
	// category, and it is one `if`.
	Uptime *float64    `json:"uptime"`
	Days   []PublicDay `json:"days"`
}

type PublicDay struct {
	Date            string `json:"date"`
	State           string `json:"state"`
	DownSeconds     int64  `json:"down_seconds"`
	DegradedSeconds int64  `json:"degraded_seconds"`
	CoveredSeconds  int64  `json:"covered_seconds"`
	// Title is the bar's tooltip, pre-rendered so the page needs no script to
	// explain itself.
	Title string `json:"title"`
}

// PublicIncident is a `down` interval. It is not a separate record: an incident
// IS an interval whose state is down, opened when the dwell was satisfied and
// closed when recovery satisfied it. `degraded` never opens one — an incident
// is a record that a service was UNAVAILABLE, and a page that files one
// whenever something is imperfect is a page that gets muted.
type PublicIncident struct {
	ID        string     `json:"id"`
	Component string     `json:"component"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Duration  string     `json:"duration"`
	Message   string     `json:"message"`
}

// Reader is the read surface a public render needs, and no more.
type Reader interface {
	ListStatusPageComponents(ctx context.Context, pageID string) ([]domain.StatusPageComponent, error)
	ListStatusIntervalsSince(ctx context.Context, componentID string, since time.Time) ([]domain.StatusInterval, error)
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetComposeStack(ctx context.Context, id string) (domain.ComposeStack, error)
	GetDatabase(ctx context.Context, id string) (domain.Database, error)
}

// Build assembles the public payload for one page.
func Build(ctx context.Context, r Reader, page domain.StatusPage, now time.Time) (PublicPage, error) {
	comps, err := r.ListStatusPageComponents(ctx, page.ID)
	if err != nil {
		return PublicPage{}, fmt.Errorf("statuspage: components: %w", err)
	}

	out := PublicPage{
		Title:      page.Title,
		Domain:     page.Domain,
		UpdatedAt:  now.UTC(),
		WindowDays: WindowDays,
		Components: []PublicComponent{},
		Incidents:  []PublicIncident{},
	}

	// Days are whole UTC days ending with today, so two visitors in different
	// zones read the same bars and the boundaries do not move.
	today := now.UTC().Truncate(24 * time.Hour)
	windowStart := today.AddDate(0, 0, -(WindowDays - 1))

	worst := ""
	for _, c := range comps {
		if !resourceExists(ctx, r, c) {
			// The read is a join: a component whose resource was deleted is
			// not rendered rather than published as a permanent unknown.
			continue
		}
		intervals, err := r.ListStatusIntervalsSince(ctx, c.ID, windowStart)
		if err != nil {
			return PublicPage{}, fmt.Errorf("statuspage: intervals: %w", err)
		}
		pc := PublicComponent{Label: c.Label, State: domain.PublicUnknown}
		if cur := currentState(intervals); cur != "" {
			pc.State = cur
		}
		pc.Days, pc.Uptime = summarize(intervals, today, now.UTC())
		out.Components = append(out.Components, pc)
		if worst == "" || domain.PublicStatusRank(pc.State) > domain.PublicStatusRank(worst) {
			worst = pc.State
		}
		out.Incidents = append(out.Incidents, incidentsOf(intervals, c.Label, now.UTC())...)
	}

	out.State, out.Headline = banner(worst, len(out.Components))
	sort.Slice(out.Incidents, func(i, j int) bool {
		return out.Incidents[i].StartedAt.After(out.Incidents[j].StartedAt)
	})
	if len(out.Incidents) > 10 {
		out.Incidents = out.Incidents[:10]
	}
	return out, nil
}

func resourceExists(ctx context.Context, r Reader, c domain.StatusPageComponent) bool {
	switch c.ResourceKind {
	case domain.StatusResourceApplication:
		_, err := r.GetApplication(ctx, c.ResourceID)
		return err == nil
	case domain.StatusResourceComposeStack:
		_, err := r.GetComposeStack(ctx, c.ResourceID)
		return err == nil
	case domain.StatusResourceDatabase:
		_, err := r.GetDatabase(ctx, c.ResourceID)
		return err == nil
	}
	return false
}

func currentState(intervals []domain.StatusInterval) string {
	for i := len(intervals) - 1; i >= 0; i-- {
		if intervals[i].EndedAt == nil {
			return intervals[i].State
		}
	}
	return ""
}

// banner ranks the components into one sentence. `unknown` deliberately
// outranks `operational`: "All systems operational" while a component is
// unreporting is the same lie in sentence form.
func banner(worst string, n int) (string, string) {
	if n == 0 {
		return domain.PublicUnknown, "No systems are being reported here yet."
	}
	switch worst {
	case domain.PublicDown:
		return domain.PublicDown, "Some systems are down"
	case domain.PublicDegraded:
		return domain.PublicDegraded, "Some systems are degraded"
	case domain.PublicUnknown:
		return domain.PublicUnknown, "Some systems are not reporting"
	default:
		return domain.PublicOperational, "All systems operational"
	}
}

// summarize turns intervals into one bar per UTC day and the uptime figure.
//
// Uptime is over OBSERVED time: grey periods are in neither the numerator nor
// the denominator, so a fleet-wide blackout cannot round itself up to 100%.
func summarize(intervals []domain.StatusInterval, today, now time.Time) ([]PublicDay, *float64) {
	days := make([]PublicDay, 0, WindowDays)
	var totalCovered, totalDown int64

	for i := WindowDays - 1; i >= 0; i-- {
		dayStart := today.AddDate(0, 0, -i)
		dayEnd := dayStart.AddDate(0, 0, 1)
		if dayEnd.After(now) {
			dayEnd = now
		}
		d := PublicDay{Date: dayStart.Format("2006-01-02"), State: domain.PublicUnknown}
		for _, in := range intervals {
			end := now
			if in.EndedAt != nil {
				end = in.EndedAt.UTC()
			}
			secs := overlap(in.StartedAt.UTC(), end, dayStart, dayEnd)
			if secs == 0 {
				continue
			}
			switch in.State {
			case domain.PublicDown:
				d.DownSeconds += secs
				d.CoveredSeconds += secs
			case domain.PublicDegraded:
				d.DegradedSeconds += secs
				d.CoveredSeconds += secs
			case domain.PublicOperational:
				d.CoveredSeconds += secs
			}
		}
		// A day is not green because the downtime was brief — it is green
		// because there was none.
		switch {
		case d.CoveredSeconds == 0:
			d.State = domain.PublicUnknown
		case d.DownSeconds > 0:
			d.State = domain.PublicDown
		case d.DegradedSeconds > 0:
			d.State = domain.PublicDegraded
		default:
			d.State = domain.PublicOperational
		}
		d.Title = dayTitle(dayStart, d)
		totalCovered += d.CoveredSeconds
		totalDown += d.DownSeconds
		days = append(days, d)
	}

	if totalCovered == 0 {
		return days, nil
	}
	up := (1 - float64(totalDown)/float64(totalCovered)) * 100
	// Two decimals, matching the design screen's 99.97%.
	up = float64(int64(up*100+0.5)) / 100
	return days, &up
}

func dayTitle(day time.Time, d PublicDay) string {
	label := day.Format("2 Jan")
	switch {
	case d.CoveredSeconds == 0:
		return label + " — not measured"
	case d.DownSeconds > 0:
		return label + " — down " + humanDuration(time.Duration(d.DownSeconds)*time.Second)
	case d.DegradedSeconds > 0:
		return label + " — degraded " + humanDuration(time.Duration(d.DegradedSeconds)*time.Second)
	default:
		return label + " — no downtime"
	}
}

func overlap(aStart, aEnd, bStart, bEnd time.Time) int64 {
	if aStart.Before(bStart) {
		aStart = bStart
	}
	if aEnd.After(bEnd) {
		aEnd = bEnd
	}
	if !aEnd.After(aStart) {
		return 0
	}
	return int64(aEnd.Sub(aStart).Seconds())
}

func incidentsOf(intervals []domain.StatusInterval, label string, now time.Time) []PublicIncident {
	var out []PublicIncident
	for _, in := range intervals {
		if in.State != domain.PublicDown {
			continue
		}
		end := now
		if in.EndedAt != nil {
			end = in.EndedAt.UTC()
		}
		out = append(out, PublicIncident{
			ID:        in.ID,
			Component: label,
			StartedAt: in.StartedAt.UTC(),
			EndedAt:   in.EndedAt,
			Duration:  humanDuration(end.Sub(in.StartedAt.UTC())),
			Message:   in.Message,
		})
	}
	return out
}

// humanDuration writes a span the way a person would say it out loud. The
// page is read by customers, not operators, so "4m 12s" beats "252s" and
// nothing here is ever a bare number of milliseconds.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %dm", s/3600, (s%3600)/60)
	default:
		return fmt.Sprintf("%dd %dh", s/86400, (s%86400)/3600)
	}
}
