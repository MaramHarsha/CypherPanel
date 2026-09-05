package protection

// Freeze-window arithmetic (deploy-protection.md §4, acceptance 8). Pure: a
// window plus an instant in, an answer out. The clock is chosen by the test,
// which is the only way to test a statement about wall clock at all
// (ENGINEERING rule 9).

import (
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

func berlin(t *testing.T, y int, m time.Month, d, hh, mm int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

// The spec's own window: Fri 18:00 → Mon 08:00 Europe/Berlin, which WRAPS the
// week. Half-open, so Friday 18:00 is already frozen and Monday 08:00 is
// already clear.
func TestInWindowWrapsTheWeek(t *testing.T) {
	w := domain.FreezeWindow{
		ID: "fzw_1", StartDOW: time.Friday, StartMinute: 18 * 60,
		EndDOW: time.Monday, EndMinute: 8 * 60, Timezone: "Europe/Berlin",
	}
	if !w.Wraps() {
		t.Fatal("Fri 18:00 → Mon 08:00 does not report as wrapping")
	}
	// 2026-08-21 is a Friday.
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"friday 17:59 — still clear", berlin(t, 2026, 8, 21, 17, 59), false},
		{"friday 18:00 — the boundary is closed", berlin(t, 2026, 8, 21, 18, 0), true},
		{"saturday noon (acceptance 8)", berlin(t, 2026, 8, 22, 12, 0), true},
		{"sunday 23:59 — across the week boundary", berlin(t, 2026, 8, 23, 23, 59), true},
		{"monday 07:59", berlin(t, 2026, 8, 24, 7, 59), true},
		{"monday 08:00 — the deploy runs (acceptance 8)", berlin(t, 2026, 8, 24, 8, 0), false},
		{"wednesday — nowhere near it", berlin(t, 2026, 8, 26, 12, 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := InWindow(w, tc.at)
			if err != nil {
				t.Fatalf("InWindow: %v", err)
			}
			if in != tc.want {
				t.Fatalf("InWindow(%s) = %v, want %v", tc.at, in, tc.want)
			}
		})
	}
}

// An ordinary window that does not wrap.
func TestInWindowOrdinary(t *testing.T) {
	w := domain.FreezeWindow{
		ID: "fzw_2", StartDOW: time.Wednesday, StartMinute: 9 * 60,
		EndDOW: time.Wednesday, EndMinute: 17 * 60, Timezone: "UTC",
	}
	if w.Wraps() {
		t.Fatal("Wed 09:00 → Wed 17:00 reported as wrapping")
	}
	at := func(d, hh, mm int) time.Time { return time.Date(2026, 8, d, hh, mm, 0, 0, time.UTC) }
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before", at(26, 8, 59), false},
		{"start", at(26, 9, 0), true},
		{"inside", at(26, 12, 0), true},
		{"end is open", at(26, 17, 0), false},
		{"another day", at(27, 12, 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := InWindow(w, tc.at)
			if err != nil || in != tc.want {
				t.Fatalf("InWindow(%s) = %v, %v; want %v", tc.at, in, err, tc.want)
			}
		})
	}
}

// The window is evaluated in ITS zone, not the plane's: the same instant is
// inside for a Berlin window and outside for a Tokyo one.
func TestInWindowUsesTheDeclaredZone(t *testing.T) {
	// Saturday 2026-08-22, 23:30 in Berlin is Sunday 06:30 in Tokyo.
	instant := berlin(t, 2026, 8, 22, 23, 30)
	sat := domain.FreezeWindow{
		ID: "fzw_sat", StartDOW: time.Saturday, StartMinute: 23 * 60,
		EndDOW: time.Saturday, EndMinute: 23*60 + 59, Timezone: "Europe/Berlin",
	}
	tokyo := sat
	tokyo.ID, tokyo.Timezone = "fzw_tokyo", "Asia/Tokyo"

	if in, err := InWindow(sat, instant); err != nil || !in {
		t.Fatalf("Berlin window = %v, %v; want inside", in, err)
	}
	if in, err := InWindow(tokyo, instant); err != nil || in {
		t.Fatalf("Tokyo window = %v, %v; want outside — it is Sunday there", in, err)
	}
}

// The lift instant is the next occurrence of the window's END in its zone, and
// it is constructed with time.Date rather than by adding hours, so a DST
// boundary does not move it off the wall-clock time the operator wrote down.
func TestLiftsAtCrossesDST(t *testing.T) {
	w := domain.FreezeWindow{
		ID: "fzw_dst", StartDOW: time.Friday, StartMinute: 18 * 60,
		EndDOW: time.Monday, EndMinute: 8 * 60, Timezone: "Europe/Berlin",
	}
	// Europe/Berlin leaves DST on Sunday 2026-10-25 at 03:00 → 02:00. A deploy
	// on the Saturday before must still be told "Mon 08:00".
	lift, err := LiftsAt(w, berlin(t, 2026, 10, 24, 12, 0))
	if err != nil {
		t.Fatalf("LiftsAt: %v", err)
	}
	if got := lift.Format("Mon 15:04"); got != "Mon 08:00" {
		t.Fatalf("lift = %s (%s), want Mon 08:00", got, lift)
	}
	if lift.Day() != 26 || lift.Month() != time.October {
		t.Fatalf("lift date = %s, want 2026-10-26", lift)
	}
}

// Asked at a moment already past this week's end, the lift is next week's.
func TestLiftsAtRollsToNextWeek(t *testing.T) {
	w := domain.FreezeWindow{
		ID: "fzw_3", StartDOW: time.Friday, StartMinute: 18 * 60,
		EndDOW: time.Monday, EndMinute: 8 * 60, Timezone: "Europe/Berlin",
	}
	// Friday evening: this week's Monday 08:00 is already behind us.
	lift, err := LiftsAt(w, berlin(t, 2026, 8, 21, 19, 0))
	if err != nil {
		t.Fatalf("LiftsAt: %v", err)
	}
	if lift.Day() != 24 || lift.Weekday() != time.Monday {
		t.Fatalf("lift = %s, want Monday 2026-08-24", lift)
	}
}

// A zone the panel cannot load is an error, never a quiet "not frozen": the
// gate turns it into a refusal (§5, fail closed).
func TestInWindowRejectsUnknownZone(t *testing.T) {
	w := domain.FreezeWindow{ID: "fzw_bad", Timezone: "Mars/Olympus", EndDOW: time.Monday}
	if _, err := InWindow(w, time.Now()); err == nil {
		t.Fatal("an unloadable zone was treated as evaluable")
	}
	if _, err := LiftsAt(w, time.Now()); err == nil {
		t.Fatal("an unloadable zone produced a lift instant")
	}
}

// The rendered sentences are what the panel, the API and a CLI all print.
func TestDescribeAndFrozenUntil(t *testing.T) {
	w := domain.FreezeWindow{
		ID: "fzw_4", StartDOW: time.Friday, StartMinute: 18 * 60,
		EndDOW: time.Monday, EndMinute: 8 * 60, Timezone: "Europe/Berlin",
	}
	if got, want := Describe(w), "Fri 18:00 → Mon 08:00 (Europe/Berlin)"; got != want {
		t.Fatalf("Describe = %q, want %q", got, want)
	}
	lift, err := LiftsAt(w, berlin(t, 2026, 8, 22, 12, 0))
	if err != nil {
		t.Fatalf("LiftsAt: %v", err)
	}
	if got, want := FrozenUntil("production", w, lift),
		"production is frozen until Mon 08:00 Europe/Berlin"; got != want {
		t.Fatalf("FrozenUntil = %q, want %q", got, want)
	}
}

// A window spanning midnight on one day still projects onto a single
// half-open minute-of-week interval.
func TestWindowMinuteProjection(t *testing.T) {
	w := domain.FreezeWindow{StartDOW: time.Tuesday, StartMinute: 90, EndDOW: time.Tuesday, EndMinute: 150}
	if got, want := w.StartOfWeekMinute(), 2*domain.MinutesPerDay+90; got != want {
		t.Fatalf("start minute-of-week = %d, want %d", got, want)
	}
	if got, want := w.EndOfWeekMinute(), 2*domain.MinutesPerDay+150; got != want {
		t.Fatalf("end minute-of-week = %d, want %d", got, want)
	}
}
