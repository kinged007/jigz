package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kinged007/jigz/internal/dirs"
	"github.com/kinged007/jigz/internal/hubstate"
)

// runStatus prints the status document of the Jigz registered for
// a repo-root. It reads the file derived from the root and does not need
// a registry entry or a live pid: a failed run leaves its last document
// behind, and a dead supervisor's document reports "stale".
func runStatus(args []string) {
	dir := ""
	asJSON := false
	for i, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case a == "--repo":
			continue
		case i > 0 && args[i-1] == "--repo":
			continue
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("unknown flag %q", a))
		default:
			dir = a
		}
	}
	if dir == "" && asJSON && hubstate.Exists() {
		out, err := hubStatusOutput()
		if err != nil {
			fatal(err)
		}
		fmt.Println(out)
		return
	}
	if dir == "" {
		dir = "."
	}
	abs, err := absRoot(dir)
	if err != nil {
		fatal(err)
	}
	out, err := statusOutput(abs, asJSON)
	if err != nil {
		if asJSON {
			msg, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Println(string(msg))
		}
		if !asJSON {
			fmt.Println(instancesSection(abs, "other instances", true))
		}
		fatal(err)
	}
	fmt.Println(out)
	if !asJSON {
		fmt.Println(instancesSection(abs, "other instances", true))
	}
}

// hubStatusOutput returns the daemon's status document, written by
// `jig daemon` next to the hub registry.
func hubStatusOutput() (string, error) {
	dir, err := dirs.State()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "hub", "status.json"))
	if err != nil {
		return "", fmt.Errorf("no daemon status document: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

func statusOutput(root string, asJSON bool) (string, error) {
	path, err := statusPath(root)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no status document for %s: %w", root, err)
	}
	var doc statusDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("status document %s: %w", path, err)
	}
	liveness := statusLiveness(doc)
	doc.Cleanup = readCleanupRecord(root)
	if !asJSON {
		return statusSummary(doc, liveness), nil
	}
	instances, _ := json.Marshal(instanceSummaries(root))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	raw["liveness"], _ = json.Marshal(liveness)
	raw["instances"] = instances
	if doc.Cleanup != nil {
		raw["cleanup"], _ = json.Marshal(doc.Cleanup)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	return string(out), err
}

func statusLiveness(doc statusDoc) string {
	switch {
	case doc.State == "exited":
		return "exited"
	case pidAlive(doc.PID):
		return "running"
	}
	return "stale"
}

func statusSummary(doc statusDoc, liveness string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Jigz %s (engine %s)\nrepo:  %s (%s)\npid:   %d\nstate: %s (%s)\n",
		doc.Version, doc.EngineVersion, doc.RepoName, doc.Repo, doc.PID, doc.State, liveness)
	for _, w := range doc.Workflows {
		exit := "-"
		if w.LastExitCode != nil {
			exit = fmt.Sprint(*w.LastExitCode)
		}
		fmt.Fprintf(&b, "  %s: %s, pid %d, restarts %d, last exit %s\n",
			w.Name, w.State, w.PID, w.RestartCount, exit)
		if line := budgetSummary(w.Budget); line != "" {
			b.WriteString("    " + line + "\n")
		}
	}
	if line := sleepSummary(doc.Sleep); line != "" {
		b.WriteString(line + "\n")
	}
	if line := cleanupSummary(doc.Cleanup); line != "" {
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// budgetSummary is the one-line form of a workflow's budget, empty when it
// has none.
func budgetSummary(b *budgetStatus) string {
	if b == nil || b.Cap == 0 {
		return ""
	}
	line := fmt.Sprintf("budget: %s of %s tokens (%s)", fmtTokens(b.TokensUsed), fmtTokens(b.Cap), b.Period)
	if b.UnmeasuredRuns > 0 {
		line += fmt.Sprintf(", %d unmeasured runs", b.UnmeasuredRuns)
	}
	if b.Paused {
		line += ", paused by " + b.PausedBy
	}
	if b.Override {
		line += ", override"
	}
	return line
}
