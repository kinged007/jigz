package main

import (
	"errors"
	"strings"
	"testing"
)

// The reported bug: `setup DIR --repo owner/name` treated the slug as the
// directory, so setup wrote .sortie/ and rewrote .gitignore in whatever
// directory the user happened to be standing in. Both orders must target
// DIR.
func TestParseArgsSetupDirectoryIsNotTheRepoSlug(t *testing.T) {
	for _, argv := range [][]string{
		{"setup", "/tmp/repo", "--repo", "owner/name"},
		{"setup", "--repo", "owner/name", "/tmp/repo"},
		{"setup", "/tmp/repo", "--repo=owner/name"},
		{"setup", "--repo=owner/name", "/tmp/repo"},
	} {
		c, err := parseArgs(argv)
		if err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		if !c.setup {
			t.Errorf("%v: setup not set", argv)
		}
		if c.dir != "/tmp/repo" {
			t.Errorf("%v: dir = %q, want /tmp/repo", argv, c.dir)
		}
		if c.repo != "owner/name" {
			t.Errorf("%v: repo = %q, want owner/name", argv, c.repo)
		}
	}
}

// A slug must not become the directory when no directory is given: that
// is how the old parser lost the working directory.
func TestParseArgsRepoAloneLeavesDirEmpty(t *testing.T) {
	c, err := parseArgs([]string{"setup", "--repo", "owner/name"})
	if err != nil {
		t.Fatal(err)
	}
	if c.dir != "" {
		t.Errorf("dir = %q, want empty so setup uses the current directory", c.dir)
	}
}

func TestParseArgsRunFlags(t *testing.T) {
	c, err := parseArgs([]string{"--unite", "--no-dashboard", "--dashboard-port=9999", "/tmp/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if c.setup || c.noServer || !c.unite || !c.noDashboard {
		t.Errorf("flags wrong: %+v", c)
	}
	if c.dashPort != 9999 {
		t.Errorf("dashPort = %d, want 9999", c.dashPort)
	}
	if c.dir != "/tmp/repo" {
		t.Errorf("dir = %q, want /tmp/repo", c.dir)
	}

	// The value may also be a separate argument.
	c, err = parseArgs([]string{"--dashboard-port", "1234"})
	if err != nil {
		t.Fatal(err)
	}
	if c.dashPort != 1234 {
		t.Errorf("dashPort = %d, want 1234", c.dashPort)
	}
}

func TestParseArgsNoArgsUsesCurrentDirectory(t *testing.T) {
	c, err := parseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.dir != "" || c.setup {
		t.Errorf("empty argv should be a plain run in the current directory, got %+v", c)
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, argv := range [][]string{
		{"--dashboard-port=notanumber"},
		{"--dashboard-port=-1"},
		{"--repo"},           // value missing
		{"--dashboard-port"}, // value missing
		{"--nope"},
	} {
		if _, err := parseArgs(argv); err == nil {
			t.Errorf("%v: expected an error", argv)
		}
	}
}

func TestParseArgsHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		if _, err := parseArgs([]string{flag}); !errors.Is(err, errHelp) {
			t.Errorf("%s: err = %v, want errHelp", flag, err)
		}
	}
}

func TestParseArgsDumpAndShowVersion(t *testing.T) {
	c, _ := parseArgs([]string{"--dump-version"})
	if !c.dumpVersion {
		t.Error("--dump-version not set")
	}
	c, _ = parseArgs([]string{"--version"})
	if !c.showVersion {
		t.Error("--version not set")
	}
}

// The usage text must name every flag the parser accepts, so the help and
// the code cannot drift apart.
func TestUsageListsEveryFlag(t *testing.T) {
	var b strings.Builder
	usageWriter = func(s string) { b.WriteString(s) }
	usage()
	usageWriter = nil
	out := b.String()
	for _, f := range []string{"--repo", "--unite", "--dashboard-port", "--no-dashboard", "--no-server", "--version", "--dump-version"} {
		if !strings.Contains(out, f) {
			t.Errorf("usage does not mention %s", f)
		}
	}
}
