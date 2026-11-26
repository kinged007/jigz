// Command explorer is an experiment: it turns a GitHub repository's issues,
// pull requests, labels, timelines and git history into a knowledge graph and
// serves a 3D explorer for it. See README.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	repo := fs.String("repo", "kinged007/saas-template", "GitHub repository owner/name")
	data := fs.String("data", "", "data directory (default .explorer/<owner>__<name>)")
	pageSize := fs.Int("page-size", 50, "sync: items per GraphQL page")
	reserve := fs.Int("reserve", 500, "sync: stop when fewer GraphQL points remain")
	maxPages := fs.Int("max-pages", 0, "sync: stop after this many pages per kind (0 = all)")
	wait := fs.Bool("wait", false, "sync: wait for the rate limit to reset instead of stopping")
	addr := fs.String("addr", "127.0.0.1:7878", "serve: listen address")
	refresh := fs.Duration("refresh", 0, "serve: sync and rebuild at this interval (0 = off)")
	_ = fs.Parse(args)

	owner, name, err := splitRepo(*repo)
	if err != nil {
		fail(err)
	}
	if *data == "" {
		*data = filepath.Join(".explorer", owner+"__"+name)
	}
	if err := os.MkdirAll(*data, 0o755); err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	so := syncOptions{Owner: owner, Name: name, PageSize: *pageSize, Reserve: *reserve, MaxPages: *maxPages, Wait: *wait}

	switch cmd {
	case "sync":
		err = doSync(ctx, *data, so)
	case "build":
		err = doBuild(*data)
	case "update":
		if err = doSync(ctx, *data, so); err == nil {
			err = doBuild(*data)
		}
	case "serve":
		var r *refresher
		if *refresh > 0 {
			r = &refresher{every: *refresh}
			go r.run(ctx, *data, so)
		}
		err = serve(ctx, *addr, *data, r)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: explorer <command> [flags]

commands:
  sync    fetch new and changed issues, PRs and timelines into SQLite; update the git clone
  build   analyse stored data and write graph.json
  update  sync, then build
  serve   serve the explorer UI (with -refresh, sync and rebuild periodically)

Run "explorer <command> -h" for flags.
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func doSync(ctx context.Context, dataDir string, o syncOptions) error {
	st, err := openStore(filepath.Join(dataDir, "github.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	gh, err := newGitHub()
	if err != nil {
		return err
	}
	if err := runSync(ctx, st, gh, o); err != nil {
		return err
	}
	var meta RepoMeta
	raw, _ := st.getMeta("repo")
	_ = json.Unmarshal([]byte(raw), &meta)
	fmt.Fprintln(os.Stderr, "git: updating clone")
	return syncGit(ctx, dataDir, o.Owner, o.Name, meta.DefaultBranch, gh.token)
}

func doBuild(dataDir string) error {
	start := time.Now()
	st, err := openStore(filepath.Join(dataDir, "github.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	g, err := buildGraph(st, dataDir)
	if err != nil {
		return err
	}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dataDir, "graph.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dataDir, "graph.json")); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "build: %d nodes, %d links, %d KB in %s\n", len(g.Nodes), len(g.Links), len(b)/1024, time.Since(start).Round(time.Millisecond))
	return nil
}
