package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestFindUniteDashboard(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	serve := func(unite bool) (*httptest.Server, int) {
		a := &app{sizes: newRepoSizes(), stateDir: t.TempDir(), unite: unite}
		srv := httptest.NewServer(a.routes(newHostGuard("127.0.0.1")))
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatal(err)
		}
		return srv, port
	}
	_, unitePort := serve(true)
	if got := findUniteDashboardOn(unitePort); got != unitePort {
		t.Errorf("unite page: got %d, want %d", got, unitePort)
	}
	_, repoPort := serve(false)
	if got := findUniteDashboardOn(repoPort); got != 0 {
		t.Errorf("single-repo page: got %d, want 0", got)
	}
}

func TestConcurrentRegistryWritesKeepEveryEntry(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := writeRegistry(fmt.Sprintf("/repo/%d", i), nil, time.Now(), dashInfo{}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i += 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dropRegistry(fmt.Sprintf("/repo/%d", i))
		}(i)
	}
	wg.Wait()
	path, _ := registryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	// A stopped run keeps its entry and loses its pid; nothing is lost.
	if len(reg.Entries) != n {
		t.Errorf("got %d entries, want %d", len(reg.Entries), n)
	}
	for i := 0; i < n; i++ {
		e, ok := reg.Entries[fmt.Sprintf("/repo/%d", i)]
		if !ok {
			t.Fatalf("entry %d was lost", i)
		}
		if stopped := e.PID == 0; stopped != (i%2 == 0) {
			t.Errorf("entry %d pid = %d", i, e.PID)
		}
	}
}

func TestClaimDashboard(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := ln.Addr().(*net.TCPAddr).Port
	if _, err := claimDashboard(busy, false); err == nil {
		t.Error("a busy dashboard port was claimed")
	}
	if got, err := claimDashboard(busy, true); err != nil || got != busy {
		t.Errorf("with no dashboard: %d, %v, want the port untouched and no error", got, err)
	}
	if got, err := claimDashboard(-busy, false); err != nil || got != -busy {
		t.Errorf("joining a live dashboard: %d, %v, want the negative port back", got, err)
	}
	ln.Close()
	if got, err := claimDashboard(busy, false); err != nil || got != busy {
		t.Errorf("a free port: %d, %v, want %d", got, err, busy)
	}
}
