package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// absRoot is the registry key for a repo-root given on the command line:
// the absolute path with symlinks resolved, so a run started through a
// link and stopped from the real directory address the same entry.
func absRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return resolvedPath(abs), nil
}

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// instance is a registry entry whose process is alive.
type instance struct {
	Root  string
	Entry registryEntry
}

func (i instance) name() string {
	if i.Entry.RepoName != "" {
		return i.Entry.RepoName
	}
	return filepath.Base(i.Root)
}

// liveInstances returns every registered run with a live process, sorted
// by root. Entries of stopped or dead runs are not listed.
func liveInstances() []instance {
	var out []instance
	for root, e := range readRegistryEntries() {
		if e.PID > 0 && pidAlive(e.PID) {
			out = append(out, instance{Root: root, Entry: e})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// otherInstances returns the live runs that do not serve root, and the
// live run that serves the shared dashboard, if any.
func otherInstances(root string) (others []instance, host *instance) {
	for _, i := range liveInstances() {
		if i.Entry.ServesDashboard && host == nil {
			i := i
			host = &i
		}
		if !samePath(i.Root, root) {
			others = append(others, i)
		}
	}
	return others, host
}

func uptime(e registryEntry) string {
	if e.StartedAt.IsZero() {
		return "-"
	}
	return time.Since(e.StartedAt).Round(time.Second).String()
}

// instancesSection lists the other live runs and the dashboard host.
func instancesSection(root, header string, withSelf bool) string {
	others, host := otherInstances(root)
	var b strings.Builder
	if len(others) == 0 {
		b.WriteString(header + ": none")
	} else {
		fmt.Fprintf(&b, "%s (%d):", header, len(others))
		for _, i := range others {
			fmt.Fprintf(&b, "\n  %s  %s  pid %d  up %s", i.name(), i.Root, i.Entry.PID, uptime(i.Entry))
			if i.Entry.ServesDashboard {
				b.WriteString("  [serves dashboard]")
			}
		}
	}
	if host != nil && (withSelf || !samePath(host.Root, root)) {
		who := fmt.Sprintf("%s (pid %d)", host.name(), host.Entry.PID)
		if samePath(host.Root, root) {
			who = "this run"
		}
		fmt.Fprintf(&b, "\ndashboard: %s, served by %s", host.Entry.Dashboard, who)
	}
	return b.String()
}

// dashboardHostWarning is printed before a run that serves the dashboard
// is stopped or restarted, and is empty for any other run.
func dashboardHostWarning(e registryEntry, action string) string {
	if !e.ServesDashboard {
		return ""
	}
	if action == "restart" {
		return fmt.Sprintf("jig: this run serves the dashboard at %s; it is unavailable until the run starts again. Other instances keep running.", e.Dashboard)
	}
	return fmt.Sprintf("jig: this run serves the dashboard at %s; the dashboard stops with it. Other instances keep running without a dashboard.", e.Dashboard)
}

// instanceSummaries is the instances array of the status JSON.
func instanceSummaries(root string) []map[string]any {
	others, _ := otherInstances(root)
	out := make([]map[string]any, 0, len(others))
	for _, i := range others {
		m := map[string]any{
			"repo_name": i.name(), "root": i.Root, "pid": i.Entry.PID,
			"started_at": i.Entry.StartedAt, "serves_dashboard": i.Entry.ServesDashboard,
		}
		if i.Entry.Dashboard != "" {
			m["dashboard"] = i.Entry.Dashboard
		}
		out = append(out, m)
	}
	return out
}
