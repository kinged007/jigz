package main

import (
	"slices"
	"syscall"
	"testing"
)

// A bare restart replaces the run rather than signalling it, so it has no
// signal; the two --when-idle forms and an immediate stop do.
func TestControlSignal(t *testing.T) {
	for _, c := range []struct {
		action   string
		whenIdle bool
		want     syscall.Signal
	}{
		{"stop", false, syscall.SIGTERM},
		{"stop", true, sigStopIdle},
		{"restart", true, sigRestartIdle},
	} {
		got, err := controlSignal(c.action, c.whenIdle)
		if err != nil {
			t.Fatalf("%s whenIdle=%v: %v", c.action, c.whenIdle, err)
		}
		if got != c.want {
			t.Errorf("%s whenIdle=%v: got %s, want %s", c.action, c.whenIdle, got, c.want)
		}
	}
	for _, c := range []struct {
		action   string
		whenIdle bool
	}{
		{"restart", false},
		{"nonsense", false},
	} {
		if _, err := controlSignal(c.action, c.whenIdle); err == nil {
			t.Errorf("%s whenIdle=%v was accepted as a signal", c.action, c.whenIdle)
		}
	}
}

// A replacing run has to keep the flags the old one ran with, and the root
// has to land last or the parser reads the other directory.
func TestRestartArgv(t *testing.T) {
	got := restartArgv(registryEntry{Args: []string{"--unite", "/old/root"}}, "/new/root")
	want := []string{"--unite", "/old/root", "/new/root"}
	if !slices.Equal(got, want) {
		t.Errorf("restartArgv = %q, want %q", got, want)
	}
	// The entry must not be written through, or append would land in the
	// registry's own slice.
	e := registryEntry{Args: []string{"--no-dashboard"}}
	restartArgv(e, "/root")
	if len(e.Args) != 1 {
		t.Errorf("restartArgv mutated the entry: args = %q", e.Args)
	}
	// An entry written before Args existed still restarts, without flags.
	if got := restartArgv(registryEntry{}, "/root"); !slices.Equal(got, []string{"/root"}) {
		t.Errorf("restartArgv with no recorded args = %q", got)
	}
}
