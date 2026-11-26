package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// refresher runs sync and build at a fixed interval and reports its state to
// the page through /status.
type refresher struct {
	every time.Duration

	mu      sync.Mutex
	nextAt  time.Time
	syncing bool
	lastErr string
}

func (r *refresher) run(ctx context.Context, dataDir string, o syncOptions) {
	for {
		r.mu.Lock()
		r.nextAt = time.Now().Add(r.every)
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.every):
		}
		r.set(true, "")
		err := doSync(ctx, dataDir, o)
		if err == nil {
			err = doBuild(dataDir)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "refresh:", err)
			r.set(false, err.Error())
			continue
		}
		r.set(false, "")
	}
}

func (r *refresher) set(syncing bool, lastErr string) {
	r.mu.Lock()
	r.syncing, r.lastErr = syncing, lastErr
	r.mu.Unlock()
}

// Status is the response of /status. Refresh is 0 when the server does not
// refresh by itself; the page then shows no live indicator and does not poll.
// Version changes whenever graph.json is rewritten.
type Status struct {
	Refresh int    `json:"refresh"`
	NextIn  int    `json:"nextIn"`
	Syncing bool   `json:"syncing"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version"`
}

func (r *refresher) status(dataDir string) Status {
	s := Status{}
	if fi, err := os.Stat(filepath.Join(dataDir, "graph.json")); err == nil {
		s.Version = strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	}
	if r == nil {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s.Refresh = int(r.every.Seconds())
	s.Syncing, s.Error = r.syncing, r.lastErr
	if !r.syncing {
		s.NextIn = max(0, int(time.Until(r.nextAt).Seconds()+0.5))
	}
	return s
}

func newMux(dataDir string, r *refresher) http.Handler {
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/graph.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, req, filepath.Join(dataDir, "graph.json"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.status(dataDir))
	})
	return mux
}

func serve(ctx context.Context, addr, dataDir string, r *refresher) error {
	srv := &http.Server{Addr: addr, Handler: newMux(dataDir, r), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "explorer: http://%s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
