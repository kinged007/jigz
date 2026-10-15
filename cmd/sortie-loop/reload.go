package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var reloadWait = 5 * time.Second

type reloadResult struct {
	Reloaded bool   `json:"reloaded"`
	Error    string `json:"error,omitempty"`
}

func reloadSupervisor(root string) (reloadResult, error) {
	e, err := runningEntry(root)
	if err != nil {
		return reloadResult{}, err
	}
	before, berr := readStatusDoc(root)
	beforeTime := time.Time{}
	if berr == nil {
		beforeTime = before.UpdatedAt
	}
	if err := syscall.Kill(e.PID, sigReload); err != nil {
		return reloadResult{}, fmt.Errorf("signal %s: %w", sigReload, err)
	}
	for deadline := time.Now().Add(reloadWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		doc, err := readStatusDoc(root)
		if err != nil {
			continue
		}
		if doc.UpdatedAt.After(beforeTime) {
			return reloadResult{Reloaded: true}, nil
		}
		if beforeTime.IsZero() && !doc.UpdatedAt.IsZero() {
			return reloadResult{Reloaded: true}, nil
		}
	}
	if _, err := runningEntry(root); err == nil {
		return reloadResult{Reloaded: true}, nil
	}
	return reloadResult{}, fmt.Errorf("the supervisor did not reload within %s", reloadWait)
}

func runReload(args []string) {
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
	res, err := reloadSupervisor(abs)
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
		if res.Reloaded {
			fmt.Printf("sortie-loop: %s reloaded\n", abs)
		} else {
			fmt.Printf("sortie-loop: %s not reloaded\n", abs)
		}
	}
}
