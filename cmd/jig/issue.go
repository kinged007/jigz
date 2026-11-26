package main

import (
	"fmt"
	"strings"

	"github.com/kinged007/jigz/internal/store"
)

// issueRestart clears the parked state of one issue after a
// non-retryable failure, so the item runs again. It is the same
// recovery path for the collector parks of #73 and the stuck-issue
// restart of #72: clearing twice never claims twice, because it writes
// nothing to the tracker. It returns the line the command prints.
func issueRestart(stateDir string, args []string) (string, error) {
	action, rest := "", args
	if len(rest) > 0 {
		action, rest = rest[0], rest[1:]
	}
	if action != "restart" {
		return "", fmt.Errorf("usage: jig issue restart <number> [--workflow NAME] [repo-root]")
	}
	var number, workflow, dir string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--workflow":
			i++
			if i >= len(rest) {
				return "", fmt.Errorf("--workflow needs a value")
			}
			workflow = rest[i]
		case strings.HasPrefix(a, "--workflow="):
			workflow = strings.TrimPrefix(a, "--workflow=")
		case strings.HasPrefix(a, "-"):
			return "", fmt.Errorf("unknown flag %q", a)
		case number == "":
			number = strings.TrimPrefix(a, "#")
		default:
			dir = a
		}
	}
	if number == "" {
		return "", fmt.Errorf("usage: jig issue restart <number> [--workflow NAME] [repo-root]")
	}
	if dir == "" {
		dir = "."
	}
	abs, err := absRoot(dir)
	if err != nil {
		return "", err
	}
	st, err := store.Open(stateDir)
	if err != nil {
		return "", err
	}
	defer st.Close()
	project := repoName(abs)
	var n int64
	if workflow != "" {
		var cleared bool
		if cleared, err = st.ClearIssuePark(project, workflow, number); err == nil && cleared {
			n = 1
		}
	} else {
		n, err = st.ClearIssueParks(project, number)
	}
	if err != nil {
		return "", err
	}
	if n == 0 {
		return fmt.Sprintf("jig: %s#%s: no active park (already clear)", project, number), nil
	}
	return fmt.Sprintf("jig: %s#%s: park cleared, the issue runs again", project, number), nil
}

// runIssueCmd runs `jig issue ...` against the history database the
// dashboard reads.
func runIssueCmd(args []string) {
	out, err := issueRestart(dashboardStateDir(), args)
	if err != nil {
		fatal(err)
	}
	fmt.Println(out)
}
