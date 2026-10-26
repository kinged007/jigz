package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

// restartBudget is how long a replaced run gets to release its ports and
// drop its registry entry before the client gives up rather than start a
// second run against the same ones.
const restartBudget = 30 * time.Second

// runRemote stops or restarts the Jigz already serving this
// repo-root, found through the shared registry.
//
// A request that only changes how the loops end has to travel as a
// signal: the running process is the only thing watching them, so it is
// the only thing that can say when one has nothing left in flight. An
// immediate restart is not that. It replaces the process, and this
// client is the only one that can start the run taking its place.
func runRemote(action string, args []string) {
	dir := "."
	whenIdle := false
	for i, a := range args {
		switch {
		case a == "--when-idle":
			whenIdle = true
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
	abs, err := absRoot(dir)
	if err != nil {
		fatal(err)
	}
	e, err := runningEntry(abs)
	if err != nil {
		fatal(err)
	}
	if w := dashboardHostWarning(e, action); w != "" {
		fmt.Println(w)
	}
	if action == "restart" && !whenIdle {
		// An immediate restart replaces the process, so it is done here
		// rather than signalled: this client is the only one that can
		// start the run that takes the old one's place, and it can do it
		// in the foreground the caller already has.
		if err := syscall.Kill(e.PID, syscall.SIGTERM); err != nil {
			fatal(fmt.Errorf("signal %s: %w", syscall.SIGTERM, err))
		}
		fmt.Printf("jig: %s (pid %d) stopped, starting again\n", abs, e.PID)
		fmt.Println(instancesSection(abs, "still running", false))
		if !waitGone(e.PID, restartBudget) {
			fatal(fmt.Errorf("%s (pid %d) is still running; not starting a second run", abs, e.PID))
		}
		// os.Executable, not os.Args[0]: a bare "jig" that found
		// us on PATH is not a path Exec can resolve.
		self, err := os.Executable()
		if err != nil {
			fatal(err)
		}
		argv := restartArgv(e, abs)
		fmt.Printf("jig: %s starting: %s %s\n", abs, self, strings.Join(argv, " "))
		// Exec replaces this process, so the new run inherits the
		// terminal the caller is holding and Ctrl-C reaches it.
		if err := syscall.Exec(self, append([]string{self}, argv...), os.Environ()); err != nil {
			fatal(fmt.Errorf("start again: %w", err))
		}
		return
	}
	sig, err := controlSignal(action, whenIdle)
	if err != nil {
		fatal(err)
	}
	if err := syscall.Kill(e.PID, sig); err != nil {
		fatal(fmt.Errorf("signal %s: %w", sig, err))
	}
	what := "restarting each loop when it goes idle"
	switch sig {
	case sigStopIdle:
		what = "stopping each loop when it goes idle"
	case syscall.SIGTERM:
		what = "stopped now"
	}
	fmt.Printf("jig: %s (pid %d) %s\n", abs, e.PID, what)
	fmt.Println(instancesSection(abs, "still running", false))
}

// restartArgv rebuilds the command line for the run replacing the old
// one. The old run's own flags come back from the registry so --unite and
// --dashboard-port survive, and the root goes last because the run parses
// the last bare argument as the directory.
func restartArgv(e registryEntry, root string) []string {
	return append(append([]string{}, e.Args...), root)
}

// waitGone blocks until pid is no longer running, or the budget is spent.
// The run being replaced is not this process's child, so it cannot be
// waited on, only polled.
func waitGone(pid int, budget time.Duration) bool {
	for deadline := time.Now().Add(budget); ; {
		if !pidAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// controlSignal maps a command to the signal a running Jigz waits
// for. A restart that is not waiting for idle is absent: it replaces the
// process instead of signalling it.
func controlSignal(action string, whenIdle bool) (syscall.Signal, error) {
	switch {
	case action == "stop" && whenIdle:
		return sigStopIdle, nil
	case action == "stop":
		return syscall.SIGTERM, nil
	case action == "restart" && whenIdle:
		return sigRestartIdle, nil
	}
	return 0, fmt.Errorf("%q cannot be signalled", action)
}

// runningEntry returns the registry entry of the live Jigz
// serving root, or an error when there is no live entry for it. A stale
// entry is worse than none: a repo whose run died leaves its row behind,
// and signalling that pid would land on an unrelated process.
func runningEntry(root string) (registryEntry, error) {
	e, err := registeredEntry(root)
	if err != nil {
		return registryEntry{}, err
	}
	if e.PID <= 0 || syscall.Kill(e.PID, 0) != nil {
		return registryEntry{}, fmt.Errorf("the loop registered for %s is no longer running", root)
	}
	return e, nil
}

// registeredEntry returns the registry entry for root whether or not its
// process is still alive.
func registeredEntry(root string) (registryEntry, error) {
	path, err := registryPath()
	if err != nil {
		return registryEntry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return registryEntry{}, fmt.Errorf("no loop is running for %s: %w", root, err)
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return registryEntry{}, fmt.Errorf("registry: %w", err)
	}
	if e, ok := reg.Entries[root]; ok {
		return e, nil
	}
	for key, e := range reg.Entries {
		if resolvedPath(key) == root {
			return e, nil
		}
	}
	return registryEntry{}, fmt.Errorf("no loop is running for %s", root)
}
