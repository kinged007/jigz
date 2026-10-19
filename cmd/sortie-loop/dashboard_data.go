package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// tableExists reports whether the DB has the named table, so fresh loop DBs
// whose migrations have not checkpointed yet read as empty, not errors.
func tableExists(db *sql.DB, name string) bool {
	var found string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&found); err != nil {
		return false
	}
	return true
}

// loopStats holds one loop's figures: live state from its /api/v1/state
// plus all-time history read directly from its SQLite DB, so totals
// survive loop restarts (the upstream dashboard reseeds from the same
// aggregate_metrics row but serves process memory, which starts at 0).
type loopStats struct {
	Endpoint loopEndpoint `json:"endpoint"`
	Live     bool         `json:"live"`
	// Live figures from /api/v1/state.
	Running         []runRow `json:"running"`
	Retrying        int      `json:"retrying"`
	BudgetExhausted int      `json:"budget_exhausted"`
	SlotsFree       int      `json:"slots_free"`
	// All-time figures from the DB.
	AllInput   int64    `json:"all_input"`
	AllOutput  int64    `json:"all_output"`
	AllTotal   int64    `json:"all_total"`
	AllCache   int64    `json:"all_cache"`
	Runs       int      `json:"runs"`
	Succeeded  int      `json:"succeeded"`
	Failed     int      `json:"failed"`
	RuntimeSec float64  `json:"runtime_sec"`
	Cost       *float64 `json:"cost,omitempty"`
	DBLive     bool     `json:"db_live"`
	DBError    string   `json:"db_error,omitempty"`
}

type runRow struct {
	Identifier string `json:"identifier"`
	State      string `json:"state"`
	Turns      int    `json:"turns"`
	Tokens     int64  `json:"tokens"`
	Model      string `json:"model"`
	StartedAt  string `json:"started_at"`
	PickUp     string `json:"picked_up,omitempty"`
}

type issueStats struct {
	Identifier string `json:"identifier"`
	RepoName   string `json:"repo_name"`
	Loop       string `json:"loop"`
	Runs       int    `json:"runs"`
	Succeeded  int    `json:"succeeded"`
	Failed     int    `json:"failed"`
	Tokens     int64  `json:"tokens"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cache_read"`
	LastStatus string `json:"last_status"`
	LastRun    string `json:"last_run"`
}

// openRO opens a sortie SQLite DB read-only without disturbing the
// writer. Plain mode=ro (not immutable=1): sortie runs in WAL mode and
// fresh tables/rows sit in the -wal sidecar until checkpoint, which an
// immutable open cannot see. A read-only query takes no write lock, and
// _query_only blocks writes through this handle.
func openRO(path string) (*sql.DB, error) {
	// The DSN is a URL, so a repository path holding ? or # would end the
	// path early and turn the rest into query parameters.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// readLoopDB fills the all-time figures from aggregate_metrics +
// run_history. Missing tables (older DBs) leave zeros, not errors.
func readLoopDB(st *loopStats) {
	db, err := openRO(st.Endpoint.DBPath)
	if err != nil {
		st.DBError = err.Error()
		return
	}
	defer db.Close()
	st.DBLive = true
	if !tableExists(db, "aggregate_metrics") || !tableExists(db, "run_history") {
		return
	}
	var updated string
	err = db.QueryRow(`SELECT input_tokens, output_tokens, total_tokens, cache_read_tokens, seconds_running, updated_at FROM aggregate_metrics WHERE key='agent_totals'`).Scan(
		&st.AllInput, &st.AllOutput, &st.AllTotal, &st.AllCache, &st.RuntimeSec, &updated)
	if err != nil && err != sql.ErrNoRows {
		st.DBError = err.Error()
		return
	}
	rows, err := db.Query(`SELECT status, COUNT(*), COALESCE(SUM(total_tokens),0) FROM run_history GROUP BY status`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		var toks int64
		if err := rows.Scan(&status, &n, &toks); err != nil {
			continue
		}
		st.Runs += n
		switch strings.ToLower(status) {
		case "succeeded":
			st.Succeeded += n
		case "failed":
			st.Failed += n
		}
		_ = toks
	}
}

// readIssueStats aggregates run_history per issue across every known DB.
func readIssueStats(endpoints []loopEndpoint) []issueStats {
	byKey := map[string]*issueStats{}
	for _, ep := range endpoints {
		if ep.DBPath == "" {
			continue
		}
		db, err := openRO(ep.DBPath)
		if err != nil {
			continue
		}
		if !tableExists(db, "run_history") {
			db.Close()
			continue
		}
		rows, err := db.Query(`SELECT display_identifier, identifier, status, input_tokens, output_tokens, total_tokens, cache_read_tokens, completed_at FROM run_history ORDER BY id`)
		if err != nil {
			db.Close()
			continue
		}
		for rows.Next() {
			var disp sql.NullString
			var ident, status, completed string
			var in, out, tot, cache int64
			if err := rows.Scan(&disp, &ident, &status, &in, &out, &tot, &cache, &completed); err != nil {
				continue
			}
			name := ident
			if disp.Valid && disp.String != "" {
				name = disp.String
			}
			key := ep.Repo + "\x00" + ep.Loop + "\x00" + name
			s, ok := byKey[key]
			if !ok {
				s = &issueStats{Identifier: name, RepoName: ep.RepoName, Loop: ep.Loop}
				byKey[key] = s
			}
			s.Runs++
			s.Input += in
			s.Output += out
			s.Tokens += tot
			s.CacheRead += cache
			s.LastStatus = status
			s.LastRun = completed
			switch strings.ToLower(status) {
			case "succeeded":
				s.Succeeded++
			case "failed":
				s.Failed++
			}
		}
		rows.Close()
		db.Close()
	}
	out := make([]issueStats, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	return out
}

func fmtTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
}

func fmtDuration(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func size(b int64) string {
	const (
		kiB = 1 << 10
		miB = 1 << 20
		giB = 1 << 30
	)
	switch {
	case b < 0:
		return "\u2014"
	case b < miB:
		return fmt.Sprintf("%.0f KiB", float64(b)/kiB)
	case b < giB:
		return fmt.Sprintf("%.0f MiB", float64(b)/miB)
	default:
		return fmt.Sprintf("%.1f GiB", float64(b)/giB)
	}
}

func cpuPct(v float64) string {
	if v < 0 {
		return "\u2014"
	}
	return fmt.Sprintf("%.0f%%", v)
}

func pct(used, total int64) string {
	if total <= 0 {
		return "\u2014"
	}
	return fmt.Sprintf("%.0f%%", float64(used)/float64(total)*100)
}

func gib(b int64) string {
	if b <= 0 {
		return "\u2014"
	}
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}

func agentLabel(kind string) string {
	switch kind {
	case "":
		return ""
	case "claude-code":
		return "Claude"
	case "pi":
		return "Pi"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	default:
		return kind
	}
}

func commas(v any) string {
	var n int64
	switch t := v.(type) {
	case int:
		n = int64(t)
	case int64:
		n = t
	default:
		return fmt.Sprint(v)
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func usd(c *float64) string {
	if c == nil {
		return "\u2014"
	}
	switch {
	case *c >= 100:
		return fmt.Sprintf("$%.0f", *c)
	case *c >= 1:
		return fmt.Sprintf("$%.2f", *c)
	case *c > 0:
		return fmt.Sprintf("$%.3f", *c)
	default:
		return "$0.00"
	}
}

func at(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "\u2014"
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

func since(started string) string {
	t, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return "\u2014"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		return fmt.Sprintf("%dd %dh", days, int(d.Hours())%24)
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func utc(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func liveHref(project, workflow, identifier string) string {
	return "/runs/" + project + "/" + workflow + "/" + identifier
}

// pickedUp is when the loop first started work on an issue on its
// current pass: the earliest attempt since the last one that succeeded.
func pickedUp(db *sql.DB, identifier string) string {
	var at sql.NullString
	err := db.QueryRow(`SELECT MIN(started_at) FROM run_history
		WHERE (display_identifier = ?1 OR identifier = ?1)
		AND id > COALESCE((SELECT MAX(id) FROM run_history
			WHERE (display_identifier = ?1 OR identifier = ?1) AND status = 'succeeded'), 0)`,
		identifier).Scan(&at)
	if err != nil || !at.Valid {
		return ""
	}
	return at.String
}
