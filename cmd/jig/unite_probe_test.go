package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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

func TestProbeLoopAPI(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"counts":{"active":0}}`)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()
	if !probeLoopAPI(port) {
		t.Errorf("answering loop API on %d not detected", port)
	}
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// A busy port serving something else (no counts payload) is not a loop.
	port2 := ln2.Addr().(*net.TCPAddr).Port
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"other":true}`)
	})
	srv2 := &http.Server{Handler: mux2}
	go srv2.Serve(ln2)
	defer srv2.Close()
	if probeLoopAPI(port2) {
		t.Errorf("non-loop page on %d misdetected as loop API", port2)
	}
	ln3, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln3.Addr().(*net.TCPAddr).Port
	ln3.Close()
	if probeLoopAPI(closed) {
		t.Errorf("closed port %d misdetected as loop API", closed)
	}
}

func TestWaitLoopBoundExitedChild(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"counts":{"active":0}}`)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// A child that exited (lost the bind race) must fail the wait even
	// though some other server answers on that port.
	dead := exec.Command("true")
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	_ = dead.Wait()
	if err := waitLoopBound(dead, port, 2*time.Second); err == nil {
		t.Error("exited child accepted as a live loop")
	}

	// A live child whose API answers passes.
	live := exec.Command("sleep", "10")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _, _ = live.Process.Wait() }()
	if err := waitLoopBound(live, port, 2*time.Second); err != nil {
		t.Errorf("live child rejected: %v", err)
	}
}

func TestLockStartsSerializes(t *testing.T) {
	a, err := lockStarts()
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	go func() {
		b, err := lockStarts()
		if err != nil {
			t.Errorf("second lock: %v", err)
			close(held)
			return
		}
		b.release()
		close(held)
	}()
	select {
	case <-held:
		t.Fatal("second lockStarts returned while the first was still held")
	case <-time.After(200 * time.Millisecond):
	}
	a.release()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("second lockStarts did not return after release")
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
			if err := writeRegistry(fmt.Sprintf("/repo/%d", i), nil, time.Now()); err != nil {
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
