package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// wakeBudget is how long the wake command waits for the supervisor to
// report that it is awake.
var wakeBudget = 5 * time.Second

type wakeResult struct {
	Woke        bool   `json:"woke"`
	WasSleeping bool   `json:"was_sleeping"`
	Error       string `json:"error,omitempty"`
}

func readStatusDoc(root string) (statusDoc, error) {
	var doc statusDoc
	path, err := statusPath(root)
	if err != nil {
		return doc, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return doc, err
	}
	return doc, json.Unmarshal(data, &doc)
}

// wakeSupervisor wakes the sleeping supervisor of root and waits for its
// status document to report awake. Awake or sleep-disabled supervisors are
// left alone.
func wakeSupervisor(root string) (wakeResult, error) {
	if _, err := runningEntry(root); err != nil {
		return wakeResult{}, err
	}
	doc, err := readStatusDoc(root)
	if err != nil {
		return wakeResult{}, fmt.Errorf("status document: %w", err)
	}
	res := wakeResult{WasSleeping: doc.Sleep.Enabled && !doc.Sleep.Awake}
	if !res.WasSleeping {
		return res, nil
	}
	if err := requestWake(root); err != nil {
		return res, err
	}
	for deadline := time.Now().Add(wakeBudget); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if d, err := readStatusDoc(root); err == nil && d.Sleep.Awake {
			res.Woke = true
			return res, nil
		}
	}
	return res, fmt.Errorf("the supervisor did not wake within %s", wakeBudget)
}

// runWake wakes the sleeping loops of the Jigz serving a repo-root.
func runWake(args []string) {
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
	res, err := wakeSupervisor(abs)
	if err != nil {
		res.Error = err.Error()
	}
	if asJSON {
		out, _ := json.Marshal(res)
		fmt.Println(string(out))
	}
	if err != nil {
		fatal(err)
	}
	if !asJSON {
		switch {
		case res.Woke:
			fmt.Printf("jig: %s woke\n", abs)
		default:
			fmt.Printf("jig: %s is not sleeping\n", abs)
		}
	}
}

// sleepSummary is the one-line form of the sleep state, empty when sleep
// is disabled.
func sleepSummary(s sleepStatus) string {
	switch {
	case !s.Enabled:
		return ""
	case s.Awake:
		return "sleep: awake"
	case s.SleepingSince == nil || s.NextWake == nil:
		return "sleep: sleeping"
	}
	return fmt.Sprintf("sleep: sleeping since %s, next wake %s", s.SleepingSince.Format(time.RFC3339), s.NextWake.Format(time.RFC3339))
}
