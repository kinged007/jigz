package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runStatus prints the status document of the sortie-loop registered for
// a repo-root. It reads the file derived from the root and does not need
// a registry entry or a live pid: a failed run leaves its last document
// behind, and a dead supervisor's document reports "stale".
func runStatus(args []string) {
	dir := "."
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
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal(err)
	}
	out, err := statusOutput(abs, asJSON)
	if err != nil {
		if asJSON {
			msg, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Println(string(msg))
		}
		fatal(err)
	}
	fmt.Println(out)
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
	if !asJSON {
		return statusSummary(doc, liveness), nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	raw["liveness"], _ = json.Marshal(liveness)
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
	fmt.Fprintf(&b, "sortie-loop %s (engine %s)\nrepo:  %s (%s)\npid:   %d\nstate: %s (%s)\n",
		doc.Version, doc.EngineVersion, doc.RepoName, doc.Repo, doc.PID, doc.State, liveness)
	for _, w := range doc.Workflows {
		exit := "-"
		if w.LastExitCode != nil {
			exit = fmt.Sprint(*w.LastExitCode)
		}
		fmt.Fprintf(&b, "  %s: %s, port %d, pid %d, restarts %d, last exit %s\n",
			w.Name, w.State, w.Port, w.PID, w.RestartCount, exit)
	}
	return strings.TrimRight(b.String(), "\n")
}
