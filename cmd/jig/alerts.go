package main

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/supervise"
)

const (
	// streakAlertMin is how many failed attempts in a row on one issue
	// raise an alert, counted over the last streakWindowDays.
	streakAlertMin   = 5
	streakWindowDays = 30
	diskAlertPercent = 90
	// dbAlertTTL bounds how often the loop databases and run history are
	// read for alerts, since the page refreshes every few seconds.
	dbAlertTTL = 10 * time.Second
)

// alert is one line of the Alerts list. Sev is err or warn.
type alert struct {
	Sev, Kind, Msg, Href string
}

// dbAlertCache holds the alerts that need a database read.
type dbAlertCache struct {
	mu  sync.Mutex
	at  time.Time
	out []alert
}

// AlertChip is the chip class of the Alerts count.
func (v opsView) AlertChip() string {
	for _, a := range v.Alerts {
		if a.Sev == "err" {
			return "failed"
		}
	}
	return "warn"
}

// projectAlerts are the alerts one project's supervisor, status document
// and loops give rise to.
func projectAlerts(p opsProject) []alert {
	href := "/projects/" + p.Slug
	var out []alert
	switch p.State {
	case supervise.StateFailed:
		msg := p.Slug + ": " + orText(p.Err, "the supervisor exited unexpectedly")
		if n := len(p.Output); n > 0 {
			msg += " (last output: " + trunc(strings.TrimSpace(p.Output[n-1]), 120) + ")"
		}
		out = append(out, alert{"err", "Failed child", msg, href})
	case supervise.StateInvalid:
		msg := p.Slug + ": " + orText(p.Err, "the workflow files did not validate")
		if len(p.Diags.Errors) > 0 {
			msg = p.Slug + ": " + p.Diags.Errors[0]
			if n := len(p.Diags.Errors) - 1; n > 0 {
				msg += fmt.Sprintf(" (and %d more)", n)
			}
		}
		out = append(out, alert{"err", "Invalid workflow", msg, href})
	}
	if st := p.Status; st != nil && p.State != supervise.StateFailed {
		switch st.Liveness {
		case loopsrc.Stale:
			out = append(out, alert{"err", "Stale supervisor", fmt.Sprintf("%s: the status document says running but process %d is gone", p.Slug, st.PID), href})
		case loopsrc.Exited:
			out = append(out, alert{"err", "Supervisor exited", p.Slug + ": the status document says it exited", href})
		}
		for _, w := range st.FailedWorkflows() {
			out = append(out, alert{"err", "Failed child", fmt.Sprintf("%s/%s: exited with code %d%s", p.Slug, w.Name, *w.LastExitCode, restartNote(w)), href})
		}
	}
	for _, l := range p.Loops {
		switch {
		case l.L.Schema != nil:
			out = append(out, alert{"warn", "Schema", fmt.Sprintf("%s/%s: %s", p.Slug, l.L.Loop, l.L.Schema), href})
		}
	}
	if p.Status != nil {
		for _, w := range p.Status.Workflows {
			if w.Agent != "" && w.Primary != "" && w.Agent != w.Primary {
				msg := fmt.Sprintf("%s/%s: on fallback agent %s", p.Slug, w.Name, w.Agent)
				if w.FallbackReason != "" {
					msg += " (" + w.FallbackReason + ")"
				}
				out = append(out, alert{"warn", "Fallback agent", msg, href})
			}
		}
	}
	return out
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// opsAlerts collects every alert for the Operations page, errors first.
func (a *app) opsAlerts(d pageData, projects []opsProject) []alert {
	var out []alert
	for _, p := range projects {
		out = append(out, projectAlerts(p)...)
		if a.enricher != nil && a.githubOn() {
			if s := a.enricher.Status(p.Slug); !s.At.IsZero() && !s.OK {
				out = append(out, alert{"warn", "GitHub enrichment", p.Slug + ": " + s.Text(), "/settings"})
			}
		}
	}
	out = append(out, a.dbAlerts(projects)...)
	out = append(out, a.capabilityAlerts()...)
	if d.Sys.DiskTotal > 0 && d.Sys.DiskUsed*100/d.Sys.DiskTotal > diskAlertPercent {
		out = append(out, alert{"warn", "Disk", "Disk use is " + pct(d.Sys.DiskUsed, d.Sys.DiskTotal) + " of " + gib(d.Sys.DiskTotal), "/settings"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sev == "err" && out[j].Sev != "err" })
	return out
}

// dbAlerts are the alerts read from the loops' databases and from run
// history. They are cached, since the page asks every few seconds.
func (a *app) dbAlerts(projects []opsProject) []alert {
	c := &a.alertCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < dbAlertTTL {
		return c.out
	}
	var out []alert
	known := map[string]bool{}
	for _, p := range projects {
		known[p.Slug] = true
		for _, l := range p.Loops {
			out = append(out, loopDBAlerts(*l.L)...)
		}
	}
	if a.store != nil {
		since := time.Now().UTC().AddDate(0, 0, -streakWindowDays).Format(tsLayout)
		streaks, err := a.store.FailureStreaks(streakAlertMin, since)
		if err == nil {
			for _, s := range streaks {
				if !known[s.Project] {
					continue
				}
				q := url.Values{"project": {s.Project}, "workflow": {s.Workflow}, "q": {s.Identifier}}
				msg := fmt.Sprintf("%s: %d failed attempts in a row", s.Identifier, s.Count)
				if s.LastError != "" {
					msg += ", last error: " + trunc(s.LastError, 100)
				}
				out = append(out, alert{"err", "Failure streak", msg, "/runs?" + q.Encode()})
			}
		}
	}
	c.at, c.out = time.Now(), out
	return out
}

// loopDBAlerts gives the budget holds and parked issues of one loop, as
// its database lists them.
func loopDBAlerts(l rollup.Loop) []alert {
	return heldAlerts(l, l.BudgetHeld, l.Parked)
}

func heldAlerts(l rollup.Loop, budget, parked []rollup.Held) []alert {
	var out []alert
	for _, q := range []struct {
		items      []rollup.Held
		kind, what string
	}{
		{budget, "Budget hold", "held by the budget"},
		{parked, "Parked issue", "parked"},
	} {
		items := slices.Clone(q.items)
		slices.SortFunc(items, func(a, b rollup.Held) int { return strings.Compare(a.ID, b.ID) })
		var parts []string
		for _, h := range items {
			item := "#" + h.ID
			if h.Reason != "" {
				item += " (" + trunc(h.Reason, 80) + ")"
			}
			parts = append(parts, item)
		}
		if len(parts) > 0 {
			out = append(out, alert{"warn", q.kind, fmt.Sprintf("%s/%s: %s %s", l.Repo, l.Loop, q.what, strings.Join(parts, ", ")), "/projects/" + l.Repo})
		}
	}
	return out
}
