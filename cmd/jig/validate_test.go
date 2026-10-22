package main

import "testing"

func TestDropRuntimeBlockWarnings(t *testing.T) {
	in := "error: x: bad\n" +
		"warning: unknown_key: claude-code: unknown top-level key \"claude-code\"\n" +
		"warning: unknown_key: extra: unknown top-level key \"extra\"\n"
	want := "error: x: bad\n" +
		"warning: unknown_key: extra: unknown top-level key \"extra\"\n"
	if got := dropRuntimeBlockWarnings(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
