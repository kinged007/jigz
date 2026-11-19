package runlive

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxSCMBytes    = 64 << 10
	maxStatusBytes = 4 << 10
)

// Locate returns the workspace directory of an issue. A candidate path,
// such as the one the engine recorded, is used when it lies in base;
// otherwise the directory is derived from the workflow and issue. The
// directory need not exist.
func Locate(base, workflow, issueID, candidate string) string {
	if candidate != "" {
		c := filepath.Clean(candidate)
		if rel, err := filepath.Rel(base, c); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return c
		}
	}
	return filepath.Join(base, sanitize(workflow), sanitize(issueID))
}

// sanitize keeps a name to one safe path element, the way the engine
// names a workspace after an issue.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

// readInside reads at most max bytes of path when, after resolving
// symlinks, it is a regular file inside base.
func readInside(base, path string, max int64) ([]byte, error) {
	rb, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if !inside(rb, rp) {
		return nil, os.ErrPermission
	}
	f, err := os.Open(rp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// Delivery is what the agent records in .sortie of its workspace about the
// pull request it delivers.
type Delivery struct {
	Branch string
	PR     int
	Owner  string
	Repo   string
	Commit string
	// Status is the value in the status file.
	Status string
}

// Empty reports whether the agent has recorded nothing.
func (d Delivery) Empty() bool { return d == Delivery{} }

// ReadDelivery reads scm.json and status from the .sortie directory of a
// workspace. Both files are optional, and base is the directory the
// workspace must lie in.
func ReadDelivery(base, workspace string) Delivery {
	var d Delivery
	if raw, err := readInside(base, filepath.Join(workspace, ".sortie", "scm.json"), maxSCMBytes); err == nil {
		var s struct {
			Branch string      `json:"branch"`
			PR     json.Number `json:"pr_number"`
			Owner  string      `json:"owner"`
			Repo   string      `json:"repo"`
			Commit string      `json:"commit"`
		}
		if json.Unmarshal(raw, &s) == nil {
			n, _ := strconv.Atoi(s.PR.String())
			d = Delivery{Branch: oneLine(s.Branch), PR: n, Owner: oneLine(s.Owner), Repo: oneLine(s.Repo), Commit: oneLine(s.Commit)}
		}
	}
	if raw, err := readInside(base, filepath.Join(workspace, ".sortie", "status"), maxStatusBytes); err == nil {
		d.Status = oneLine(string(raw))
	}
	return d
}

func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s, _ = clip(s, MaxText)
	return s
}

// ResumeCommand is the shell command that reopens a session in its
// workspace, or "" when the agent kind has none or the id is unusable.
func ResumeCommand(kind, workspace, sessionID string) string {
	if !ValidID(sessionID) || workspace == "" {
		return ""
	}
	cd := "cd " + shellQuote(workspace) + " && "
	switch kind {
	case Claude:
		return cd + "claude -r " + sessionID
	case Pi:
		return cd + "pi --session " + sessionID
	}
	return ""
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
