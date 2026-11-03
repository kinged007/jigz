// Command schemagen records the database schema of one engine release.
// It runs the release with --dry-run against a stub tracker so no agent
// runs, then dumps the created database's tables. The fixture feeds the
// schema contract test: a release without a fixture fails it.
//
//	engine=<repo>@<tag> (required), out=<dir> (default below)
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/engine"
	_ "modernc.org/sqlite"
)

var docBody = `---
tracker:
  kind: github
  api_key: testkey12345678
  project: owner/repo
  query_filter: "label:x -label:needs-human"
  active_states: [x, in-progress]
  in_progress_state: in-progress
  handoff_state: agent:done
  terminal_states: [closed]
  comments:
    on_completion: false
polling:
  interval_ms: 5000
db_path: ../data/jigz-fixture.db
workspace:
  root: %s
  retention_days: 30
hooks:
  after_create: |
    true
  before_run: |
    true
  timeout_ms: 60000
agent:
  kind: mock
  command: /bin/true
  max_turns: 1
  max_concurrent_agents: 1
mock:
  model: "m"
dispatch:
  rules:
    - name: r
      match:
        labels: ["x"]
      template: ./prompts/p.md
  default:
    template: ./prompts/p.md
---
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

func run() error {
	engineFlag := flag.String("engine", "", "release as <repo>@<tag>")
	outDir := flag.String("out", "internal/rollup/testdata/schema", "fixture directory")
	flag.Parse()
	repo, tag, ok := strings.Cut(*engineFlag, "@")
	if !ok || repo == "" || tag == "" {
		return fmt.Errorf("usage: schemagen -engine <repo>@<tag> [-out dir]")
	}
	spec := engine.Spec{Repo: repo, Tag: tag}
	bin := engine.Installed(engine.ShareDirs(), spec)
	if bin == "" {
		var err error
		if bin, err = engine.Fetch(spec); err != nil {
			return fmt.Errorf("engine %s: %w", spec, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"total_count":0,"items":[]}`
		if strings.Contains(r.URL.Path, "/labels") {
			body = `[]`
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	root, err := os.MkdirTemp("", "schemagen-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	wf := filepath.Join(root, "w")
	if err := os.MkdirAll(filepath.Join(wf, "prompts"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wf, "prompts", "p.md"), []byte("# prompt\n"), 0o644); err != nil {
		return err
	}
	ws := filepath.Join(root, "ws", "probe")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return err
	}
	// The engine opens the database but does not create its directory.
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wf, "WORKFLOW.fixture.md"), []byte(fmt.Sprintf(docBody, ws)), 0o644); err != nil {
		return err
	}
	// A real run creates the database; --dry-run does not touch it.
	// Twenty seconds of an empty tracker is plenty for the migrations.
	// The loop is idle the whole time: no tracker item matches, so no
	// agent runs.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--port", "0", filepath.Join(wf, "WORKFLOW.fixture.md"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + root,
		"SORTIE_TRACKER_ENDPOINT=" + srv.URL,
		"SORTIE_TRACKER_PROJECT=owner/repo",
		"SORTIE_TRACKER_API_KEY=testkey12345678",
		"JIG_WORKSPACES=" + root,
	}
	if out, err := cmd.CombinedOutput(); err != nil && ctx.Err() != context.DeadlineExceeded {
		return fmt.Errorf("engine run: %w\n%s", err, out)
	}
	return dump(filepath.Join(root, "data", "jigz-fixture.db"), *outDir)
}

// dump writes the database's effective tables and schema version to
// <out>/<version>.sql, one CREATE TABLE per table in name order. The
// tables are reconstructed from PRAGMA table_info so columns added by
// later migrations are included; indexes and triggers are omitted,
// they carry no contract.
func dump(dbPath, outDir string) error {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- Schema of the database an engine run creates.\n-- Recorded with: make schema-fixture ENGINE=<repo>@<tag>\n")
	for _, t := range tables {
		stmt, err := createTable(db, t)
		if err != nil {
			return err
		}
		b.WriteString(stmt + "\n")
	}
	fmt.Fprintf(&b, "INSERT INTO schema_migrations VALUES (%d, '');\n", version)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(outDir, fmt.Sprintf("%d.sql", version))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

// createTable reconstructs the current CREATE TABLE from PRAGMA
// table_info, including columns later migrations added.
func createTable(db *sql.DB, table string) (string, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type col struct {
		name    string
		ctype   string
		notnull bool
		dflt    sql.NullString
		pk      bool
	}
	var cols []col
	pks := 0
	for rows.Next() {
		var c col
		var cid, pk int
		if err := rows.Scan(&cid, &c.name, &c.ctype, &c.notnull, &c.dflt, &pk); err != nil {
			return "", err
		}
		c.pk = pk > 0
		if c.pk {
			pks++
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	// AUTOINCREMENT survives only in sqlite_sequence; detect it so the
	// reconstructed key behaves the same.
	autoincrement := map[string]bool{}
	if seq, err := db.Query(`SELECT name FROM sqlite_sequence`); err == nil {
		for seq.Next() {
			var n string
			if err := seq.Scan(&n); err == nil {
				autoincrement[n] = true
			}
		}
		seq.Close()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", table)
	for i, c := range cols {
		part := fmt.Sprintf("    %s %s", c.name, c.ctype)
		if c.pk && pks == 1 && strings.ToUpper(c.ctype) == "INTEGER" {
			part += " PRIMARY KEY"
			if autoincrement[table] {
				part += " AUTOINCREMENT"
			}
		} else {
			if c.notnull {
				part += " NOT NULL"
			}
			if c.dflt.Valid {
				part += " DEFAULT " + c.dflt.String
			}
		}
		if i < len(cols)-1 || pks > 1 {
			part += ","
		}
		b.WriteString(part + "\n")
	}
	if pks > 1 {
		var keys []string
		for _, c := range cols {
			if c.pk {
				keys = append(keys, c.name)
			}
		}
		b.WriteString(fmt.Sprintf("    PRIMARY KEY (%s)\n", strings.Join(keys, ", ")))
	}
	b.WriteString(");")
	return b.String(), nil
}
