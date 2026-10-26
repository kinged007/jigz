package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	machine "github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
)

// resolveRepo maps a slug to the repository root and registry entry the
// control actions address. In a single-repo run the live supervisor is
// authoritative; a unite page looks every slug up in the registry.
func resolveRepo(slug string, unite bool, sup *supervisor) (string, registryEntry, bool) {
	if !unite && sup != nil && (slug == "" || slug == repoName(sup.root)) {
		return sup.root, registryEntry{PID: os.Getpid(), RepoName: repoName(sup.root)}, true
	}
	for root, e := range readRegistryEntries() {
		name := e.RepoName
		if name == "" {
			name = repoName(root)
		}
		if slug == "" || slug == name {
			return root, e, true
		}
	}
	return "", registryEntry{}, false
}

// selfBinary is the Jigz binary to run for wake and cleanup.
func selfBinary() string {
	if self, err := os.Executable(); err == nil {
		return self
	}
	return "jig"
}

// statusDir is where each repository's status document lives.
func statusDir() string {
	if p, err := registryPath(); err == nil {
		return filepath.Join(filepath.Dir(p), "status")
	}
	return ""
}

// dashboardStateDir is the directory whose filesystem the disk figure
// should describe.
func dashboardStateDir() string {
	if p, err := registryPath(); err == nil {
		return filepath.Dir(p)
	}
	return "/"
}

// serveDashboard runs the fleet dashboard in the background.
//
// The page is bound to loopback by default; --host moves it and keeps
// the Host check unless the address is a wildcard.
func serveDashboard(port int, unite bool, sup *supervisor, hostFlag string) {
	host := hostFlag
	if host == "" {
		host = "127.0.0.1"
	}
	g := newHostGuard(host)

	stateDir := dashboardStateDir()
	a := &app{
		sup:       &fleetSupervisor{local: sup, localOnly: !unite},
		sys:       machine.New(stateDir),
		sizes:     newRepoSizes(),
		stateDir:  stateDir,
		applyWait: 30 * time.Second,
		pollEvery: 500 * time.Millisecond,
		bindAddr:  bindLabel(g, port),
		exposed:   g.wildcard || !isLoopbackHost(host),
		unite:     unite,
	}
	a.sup.app = a
	a.source = loopsrc.StatusDoc{
		Reader: rollup.Reader{},
		Roots:  a.projectRoots,
		Dir:    statusDir(),
		Binary: selfBinary(),
	}
	a.configPath = filepath.Join(sup.root, ".jigz", "config.yaml")

	if a.exposed {
		fmt.Fprintf(os.Stderr, "jig: dashboard exposed beyond loopback on %s: it has no authentication, so keep it on a private network or behind a secure proxy\n", bindLabel(g, port))
	}
	go a.watchSizes(make(chan struct{}))
	// The process exit closes the history database's connections, which
	// SQLite tolerates.
	a.startHistory()
	fmt.Printf("dashboard http://%s (bound to %s)\n", browseAddr(g, port), bindLabel(g, port))
	// The server must not block the caller: main runs the supervisor loop.
	go func() {
		if err := serve(a, g, port); err != nil {
			fmt.Fprintln(os.Stderr, "jig: dashboard:", err)
		}
	}()
}

// bindLabel names the address the listener binds.
func bindLabel(g hostGuard, port int) string {
	addr := g.bindAddr()
	switch {
	case g.wildcard && g.v6:
		return fmt.Sprintf("[::]:%d (all interfaces)", port)
	case g.wildcard:
		return fmt.Sprintf("0.0.0.0:%d (all interfaces)", port)
	case addr == "":
		addr = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", addr, port)
}

// browseAddr is an address a browser on this machine can open.
func browseAddr(g hostGuard, port int) string {
	addr := g.bindAddr()
	if addr == "" {
		addr = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", addr, port)
}
