package rest

// Guided onboarding (guided-onboarding.md §5).
//
// One route and one GET. There is deliberately no dismiss: progress is DERIVED
// from what exists, so there is no flag to clear — and a panel that has walked
// the path stops showing the band because the derivation says so, not because
// someone ticked something.
//
// Member rank: everyone who can see the panel can see how far it is set up, and
// the answer holds no credential and no name — four booleans and four counts.
// What an individual step's BUTTON does is gated by that action's own rank, as
// it already is.

import (
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

type onboardingStepDTO struct {
	Name     string `json:"name"`
	Complete bool   `json:"complete"`
	Count    int64  `json:"count"`
}

type onboardingDTO struct {
	Steps []onboardingStepDTO `json:"steps"`
	// Done is what the UI renders on: the band appears only while it is false.
	Done bool `json:"done"`
}

func (a *API) handleGetOnboarding(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleMember) {
		return
	}
	if a.deps.Onboarding == nil || a.deps.OnboardingCounts == nil {
		// Nothing to guide with. Answering "done" is the honest degradation:
		// a band that cannot know what is left must not claim work remains.
		writeJSON(w, http.StatusOK, onboardingDTO{Done: true, Steps: []onboardingStepDTO{}})
		return
	}
	p, err := a.deps.Onboarding.Progress(r.Context(), a.deps.OnboardingCounts)
	if err != nil {
		a.deps.Log.Error("reading onboarding progress", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read setup progress")
		return
	}
	out := onboardingDTO{Done: p.Done, Steps: make([]onboardingStepDTO, 0, len(p.Steps))}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, onboardingStepDTO{Name: s.Name, Complete: s.Complete, Count: s.Count})
	}
	writeJSON(w, http.StatusOK, out)
}
