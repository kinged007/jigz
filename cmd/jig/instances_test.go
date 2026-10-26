package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEntries(t *testing.T, entries map[string]registryEntry) {
	t.Helper()
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
	path, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(registry{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, string(data))
}

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func fleetEntries(t *testing.T, hostPID, joinerPID int) map[string]registryEntry {
	started := time.Now().Add(-90 * time.Minute)
	return map[string]registryEntry{
		"/work/host":    {RepoName: "o/host", PID: hostPID, StartedAt: started, Dashboard: "http://127.0.0.1:7677", ServesDashboard: true},
		"/work/joiner":  {RepoName: "o/joiner", PID: joinerPID, StartedAt: started, Dashboard: "http://127.0.0.1:7677"},
		"/work/dead":    {RepoName: "o/dead", PID: deadPID(t), StartedAt: started},
		"/work/stopped": {RepoName: "o/stopped"},
	}
}

func TestInstancesSectionListsLiveOnly(t *testing.T) {
	writeEntries(t, fleetEntries(t, os.Getpid(), os.Getppid()))
	out := instancesSection("/work/joiner", "other instances", true)
	for _, want := range []string{"other instances (1):", "o/host  /work/host  pid", "up 1h30m", "[serves dashboard]",
		"dashboard: http://127.0.0.1:7677, served by o/host"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"o/dead", "o/stopped", "o/joiner"} {
		if strings.Contains(out, bad) {
			t.Errorf("lists %q:\n%s", bad, out)
		}
	}
}

func TestInstancesSectionSelfHost(t *testing.T) {
	writeEntries(t, fleetEntries(t, os.Getpid(), os.Getppid()))
	if out := instancesSection("/work/host", "other instances", true); !strings.Contains(out, "served by this run") || !strings.Contains(out, "o/joiner") {
		t.Errorf("self host:\n%s", out)
	}
	if out := instancesSection("/work/host", "still running", false); strings.Contains(out, "dashboard:") {
		t.Errorf("a stopping host is named as serving:\n%s", out)
	}
}

func TestInstancesSectionNone(t *testing.T) {
	writeEntries(t, map[string]registryEntry{"/work/self": {PID: os.Getpid()}})
	if out := instancesSection("/work/self", "other instances", true); out != "other instances: none" {
		t.Errorf("out = %q", out)
	}
}

func TestStatusWithoutRunStillListsInstances(t *testing.T) {
	writeEntries(t, fleetEntries(t, os.Getpid(), os.Getppid()))
	var err error
	out := captureStdout(t, func() {
		defer func() {
			if r := recover(); r != nil {
				err = r.(fatalError).err
			}
		}()
		runStatus([]string{t.TempDir()})
	})
	if err == nil || !strings.Contains(err.Error(), "no status document") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(out, "o/host") || !strings.Contains(out, "o/joiner") {
		t.Errorf("overview missing:\n%s", out)
	}
}

func TestStatusJSONGainsInstances(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	out, err := statusOutput(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Liveness  string           `json:"liveness"`
		Instances []map[string]any `json:"instances"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Liveness != "running" || got.Instances == nil {
		t.Errorf("unexpected document: %s", out)
	}
}

func TestStopWarnsWhenHostAndListsOthers(t *testing.T) {
	host := sleeper(t)
	entries := fleetEntries(t, host.Process.Pid, os.Getppid())
	root := t.TempDir()
	entries[root] = entries["/work/host"]
	delete(entries, "/work/host")
	writeEntries(t, entries)
	out := captureStdout(t, func() { runRemote("stop", []string{root}) })
	for _, want := range []string{"the dashboard stops with it", "Other instances keep running", "stopped now", "still running (1):", "o/joiner  /work/joiner"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dashboard: ") {
		t.Errorf("names the stopping host as serving:\n%s", out)
	}
}

func TestStopOfJoinerDoesNotWarn(t *testing.T) {
	joiner := sleeper(t)
	entries := fleetEntries(t, os.Getpid(), joiner.Process.Pid)
	root := t.TempDir()
	entries[root] = entries["/work/joiner"]
	delete(entries, "/work/joiner")
	writeEntries(t, entries)
	out := captureStdout(t, func() { runRemote("stop", []string{root}) })
	if strings.Contains(out, "serves the dashboard") {
		t.Errorf("warned:\n%s", out)
	}
	if !strings.Contains(out, "dashboard: http://127.0.0.1:7677, served by o/host") {
		t.Errorf("host not named:\n%s", out)
	}
}

func TestRegistryDashboardFieldsAreOptional(t *testing.T) {
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
	if err := writeRegistry("/r", nil, time.Now(), dashInfo{}); err != nil {
		t.Fatal(err)
	}
	path, _ := registryPath()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "dashboard") {
		t.Errorf("empty dashboard written: %s", data)
	}
	if err := writeRegistry("/r", nil, time.Now(), dashInfo{URL: "http://127.0.0.1:7677", Serve: true}); err != nil {
		t.Fatal(err)
	}
	if e, _ := registeredEntry("/r"); e.Dashboard != "http://127.0.0.1:7677" || !e.ServesDashboard {
		t.Errorf("entry = %+v", e)
	}
	dropRegistry("/r")
	if e, _ := registeredEntry("/r"); e.Dashboard != "" || e.ServesDashboard || e.PID != 0 {
		t.Errorf("after exit: %+v", e)
	}
}

func TestRootLookupThroughSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	realKey, _ := filepath.EvalSymlinks(real)
	fromLink, err := absRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	if fromLink != realKey {
		t.Errorf("absRoot(link) = %q, want %q", fromLink, realKey)
	}
	writeEntries(t, map[string]registryEntry{realKey: {PID: os.Getpid()}})
	if _, err := runningEntry(fromLink); err != nil {
		t.Errorf("lookup through the link: %v", err)
	}
	writeEntries(t, map[string]registryEntry{link: {PID: os.Getpid()}})
	if _, err := runningEntry(realKey); err != nil {
		t.Errorf("legacy link-keyed entry: %v", err)
	}
}
