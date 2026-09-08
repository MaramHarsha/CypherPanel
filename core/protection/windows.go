// Package protection is deploy protection (docs/features/deploy-protection.md):
// one admission check on one transition. An Environment may declare WHO must
// approve a deploy there and WHEN deploys are not allowed at all; the plane
// consults the policy once, where a Deployment is born, and before any work
// item reaches an agent.
//
// It holds three things that stay deliberately separate:
//
//   - the arithmetic (this file): pure, clock-in / answer-out freeze-window
//     evaluation with no store and no policy — minute-of-week in the window's
//     OWN zone, half-open, allowed to wrap the week (§4);
//   - the gate (gate.go): the policy read that turns a window list plus a
//     break-glass grant into a domain.DeployAdmission the scheduler acts on;
//   - the service (protection.go): the document CRUD, the approval decisions
//     and the recorded overrides behind the REST surface (§6).
package protection

import (
	"fmt"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// InWindow reports whether now falls inside w.
//
// Evaluation is minute-of-week arithmetic in the window's own zone: an ordinary
// window matches start <= now < end, and one that WRAPS the week (Fri 18:00 →
// Mon 08:00) matches now >= start OR now < end. Half-open, so Mon 08:00 is
// already clear.
//
// Wall clock in the declared zone is deliberate (§4): a DST change makes the
// window an hour shorter or longer in absolute terms twice a year, which is
// exactly what "no deploys after six on Friday" means to whoever wrote it. An
// absolute-instant window would drift off the working day.
func InWindow(w domain.FreezeWindow, now time.Time) (bool, error) {
	loc, err := locationOf(w)
	if err != nil {
		return false, err
	}
	cur := minuteOfWeek(now.In(loc))
	start, end := w.StartOfWeekMinute(), w.EndOfWeekMinute()
	if w.Wraps() {
		return cur >= start || cur < end, nil
	}
	return cur >= start && cur < end, nil
}

// LiftsAt returns the first instant at or after now when w stops applying —
// what the 409 body names so a refused deploy says when to come back.
//
// The next occurrence of the window's end is constructed with time.Date in the
// window's zone rather than by adding 24h multiples, because a day is not
// always 24 hours there: across a DST boundary, arithmetic on the instant would
// name a wall-clock time an hour off the one the operator wrote down.
func LiftsAt(w domain.FreezeWindow, now time.Time) (time.Time, error) {
	loc, err := locationOf(w)
	if err != nil {
		return time.Time{}, err
	}
	local := now.In(loc)
	deltaDays := int(w.EndDOW) - int(local.Weekday())
	y, m, d := local.Date()
	lift := time.Date(y, m, d+deltaDays, w.EndMinute/60, w.EndMinute%60, 0, 0, loc)
	if !lift.After(local) {
		lift = time.Date(y, m, d+deltaDays+7, w.EndMinute/60, w.EndMinute%60, 0, 0, loc)
	}
	return lift, nil
}

// Describe renders a window as the sentence the screens and the 409 body use:
// "Fri 18:00 → Mon 08:00 (Europe/Berlin)". Composed here so a CLI, the API and
// the panel print the same words (CLAUDE.md rule 4).
func Describe(w domain.FreezeWindow) string {
	return fmt.Sprintf("%s %s → %s %s (%s)",
		w.StartDOW.String()[:3], clock(w.StartMinute),
		w.EndDOW.String()[:3], clock(w.EndMinute),
		w.Timezone)
}

// FrozenUntil is the refusal sentence: which environment, and when it lifts, in
// the window's own zone — "production is frozen until Mon 08:00 Europe/Berlin".
func FrozenUntil(environmentName string, w domain.FreezeWindow, lift time.Time) string {
	return fmt.Sprintf("%s is frozen until %s %s", environmentName,
		lift.Format("Mon 15:04"), w.Timezone)
}

// minuteOfWeek projects a local time onto 0…10079, the single coordinate a
// window is compared in. Seconds are truncated: a window boundary is a minute.
func minuteOfWeek(t time.Time) int {
	return int(t.Weekday())*domain.MinutesPerDay + t.Hour()*60 + t.Minute()
}

// clock renders minutes past midnight as HH:MM.
func clock(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// locationOf resolves a window's IANA zone. cypherd embeds the IANA database
// (`_ "time/tzdata"` in core/cmd/cypherd/main.go), so this does not depend on
// the host image carrying /usr/share/zoneinfo (§4) — but a zone name that is
// simply wrong still fails here, and the gate treats that as a refusal, never
// as a pass (§5, fail closed).
func locationOf(w domain.FreezeWindow) (*time.Location, error) {
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return nil, fmt.Errorf("protection: loading time zone %q for window %s: %w", w.Timezone, w.ID, err)
	}
	return loc, nil
}
