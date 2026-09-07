package main

// The nav is EDITORIAL, so it is written down (documentation-site.md §3).
//
// Canvas 19a groups the contents in a way no directory listing produces — it
// pulls first-run-setup next to the deployment guide under "Getting started",
// and api-tokens next to outbound-webhooks under "Automate". That ordering is a
// judgement about what a reader needs first, so it lives here in one ordered
// map rather than being inferred from the tree.
//
// Two invariants hold it honest, and nav_test.go asserts both:
//
//   - every publishable document appears in exactly ONE group — a duplicate is
//     as much a failure as an omission, because a page reachable from two
//     places has two URLs and neither is canonical;
//   - every file in docs/ is either placed here or named in `excluded`, so a
//     new spec that nobody filed fails the build rather than silently vanishing
//     from the site.

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// group is one heading in the contents and one block in the article sidebar.
type group struct {
	Title string
	// Paths are docs-relative ("features/routing-and-tls.md"), in reading order.
	Paths []string
}

// nav is the whole published site, in the order canvas 19a prints it.
var nav = []group{
	{Title: "Getting started", Paths: []string{
		"dev/deployment.md",
		"features/first-run-setup.md",
		"features/local-server.md",
		"features/guided-onboarding.md",
	}},
	{Title: "Concepts", Paths: []string{
		"architecture.md",
		"features/projects-and-environments.md",
		"vision.md",
		"tech-stack.md",
		"project-structure.md",
		"glossary.md",
		"features/documentation-site.md",
	}},
	{Title: "Templates", Paths: []string{
		"features/template-catalog.md",
	}},
	{Title: "Build & deploy", Paths: []string{
		"features/build-detection.md",
		"features/pack-builds.md",
		"features/builder-role-and-relay.md",
		"features/deploy-key-private-repos.md",
		"features/application-deploy.md",
		"features/deployment-control.md",
		"features/preview-environments.md",
		"features/revision-promotion.md",
		"features/deploy-protection.md",
		"features/scheduled-tasks.md",
		"features/compose-stacks.md",
		"features/shared-variables.md",
		"features/app-scaling.md",
	}},
	{Title: "Data & storage", Paths: []string{
		"features/managed-databases.md",
		"features/volume-backups.md",
		"features/disk-management.md",
		"features/project-export.md",
	}},
	{Title: "Networking & domains", Paths: []string{
		"features/routing-and-tls.md",
		"features/dns-automation.md",
		"features/app-access-control.md",
		"features/registries.md",
		"features/managed-email.md",
		"features/panel-mail.md",
	}},
	{Title: "Access & security", Paths: []string{
		"features/teams-and-roles.md",
		"features/invitations-and-access-requests.md",
		"features/two-factor-auth.md",
		"features/session-management.md",
		"features/agent-identity-and-tls.md",
		"features/control-plane-hardening.md",
		"security/threat-model.md",
	}},
	{Title: "Operate", Paths: []string{
		"features/agent-updates.md",
		"features/panel-updates.md",
		"features/plane-disaster-recovery.md",
		"features/metrics-and-usage.md",
		"features/threshold-alerts.md",
		"features/log-drains.md",
		"features/bounded-log-retention.md",
		"features/notifications.md",
		"features/notification-inbox.md",
		"features/audit-log.md",
		"features/resource-quotas.md",
		"features/status-pages.md",
	}},
	{Title: "Automate", Paths: []string{
		"features/in-panel-api-reference.md",
		"features/api-tokens.md",
		"features/outbound-webhooks.md",
		"dev/release-signing.md",
	}},
}

// adrGroup is drawn separately on the home page — canvas 19a gives the twelve
// decisions their own block, numbered, under "why the panel is built this way".
// The list is derived from the directory rather than typed: ADRs are numbered
// and sort correctly, and a thirteenth must appear without anyone remembering.
const adrDir = "adrs"

// excluded is the team's working notes, and the exclusion is the editorial half
// of this feature (documentation-site.md §2).
//
// An operator reading "how do I restore a backup" is not served by a document
// that argues about which screens are still unbuilt, and publishing a roadmap
// invites reading it as a promise. `dev/` splits: the deployment guide and the
// release-signing procedure are things an operator DOES, so they are published
// above; CI wiring, the review bot and a 1,700-line machine-generated import
// report are not.
var excluded = []string{
	"roadmap.md",
	"product/",
	"dev/ci.md",
	"dev/review-bot.md",
	"dev/template-import.md",
	"dev/template-import-report.md",
}

func isExcluded(rel string) bool {
	for _, e := range excluded {
		if strings.HasSuffix(e, "/") {
			if strings.HasPrefix(rel, e) {
				return true
			}
			continue
		}
		if rel == e {
			return true
		}
	}
	return false
}

// Section is the URL prefix a document is published under. Three of them, and
// the split is the reader's question rather than the repository's layout: "how
// do I", "why is it like this", "what is this word".
const (
	sectionGuides    = "guides"
	sectionDecisions = "decisions"
	sectionReference = "reference"
)

// sectionOf decides where a document is published from its path alone.
func sectionOf(rel string) string {
	switch {
	case strings.HasPrefix(rel, "features/"):
		return sectionGuides
	case strings.HasPrefix(rel, adrDir+"/"):
		return sectionDecisions
	default:
		return sectionReference
	}
}

// slugOf is the last URL segment: the file name, lowercased, extension dropped.
// ADR file names already carry their number, which is what makes the decisions
// sort and read correctly without a separate ordinal.
func slugOf(rel string) string {
	base := path.Base(rel)
	return strings.ToLower(strings.TrimSuffix(base, ".md"))
}

// urlOf is the canonical directory URL for a document. Directory URLs with an
// index.html inside, so the address bar carries no ".html" and a link written
// today survives the site moving between hosts (§4).
func urlOf(rel string) string {
	return "/" + sectionOf(rel) + "/" + slugOf(rel) + "/"
}

// navIndex maps a docs-relative path to the group that claims it, and refuses a
// path claimed twice.
func navIndex() (map[string]string, error) {
	out := make(map[string]string)
	for _, g := range nav {
		for _, p := range g.Paths {
			if prev, dup := out[p]; dup {
				return nil, fmt.Errorf("docs-site: %s is in both %q and %q; a page reachable from two places has two URLs and neither is canonical", p, prev, g.Title)
			}
			out[p] = g.Title
		}
	}
	return out, nil
}

// navPaths is every path the nav claims, sorted — for the completeness check.
func navPaths() []string {
	var out []string
	for _, g := range nav {
		out = append(out, g.Paths...)
	}
	sort.Strings(out)
	return out
}
