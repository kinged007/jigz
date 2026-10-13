package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestFindUniteDashboard(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<title>Sortie Loop \u2014 Unite</title>")
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// Temporarily narrow the scan range to our ephemeral port by
	// checking the marker logic directly: a unite page is found,
	// a loop page is not.
	if got := findUniteDashboardOn(port); got != port {
		t.Errorf("unite page: got %d, want %d", got, port)
	}
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port2 := ln2.Addr().(*net.TCPAddr).Port
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<title>Sortie Dashboard</title>")
	})
	srv2 := &http.Server{Handler: mux2}
	go srv2.Serve(ln2)
	defer srv2.Close()
	if got := findUniteDashboardOn(port2); got != 0 {
		t.Errorf("non-unite page: got %d, want 0", got)
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
	if len(reg.Entries) != n/2 {
		t.Errorf("got %d entries, want %d", len(reg.Entries), n/2)
	}
	for i := 1; i < n; i += 2 {
		if _, ok := reg.Entries[fmt.Sprintf("/repo/%d", i)]; !ok {
			t.Errorf("entry %d was lost", i)
		}
	}
}
