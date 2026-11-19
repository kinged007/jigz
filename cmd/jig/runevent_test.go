package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/runlive"
)

type eventReply struct {
	Text  string `json:"text"`
	More  int    `json:"more"`
	Limit int    `json:"limit"`
}

func eventPath(base, ref string) string {
	return base + "/agent-log/event?ref=" + url.QueryEscape(ref)
}

func (r *liveRig) appendTranscript(t *testing.T, line string) {
	t.Helper()
	f, err := os.OpenFile(r.transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func (r *liveRig) addRows(t *testing.T, queries ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentEventRouteReturnsTheFullTextOfALiveEvent(t *testing.T) {
	r := newLiveRig(t, "")
	base := "/runs/o/p/issues/live-7"
	big := strings.Repeat("0123456789", 30000)
	r.appendTranscript(t, `{"type":"assistant","timestamp":"2026-01-01T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"`+big+`"}]}}`)
	mid := strings.Repeat("m", 5000)
	r.appendTranscript(t, `{"type":"assistant","timestamp":"2026-01-01T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"text","text":"`+mid+`"}]}}`)
	evs, err := runlive.TailEvents(r.transcript, runlive.Claude, runlive.MaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	var bigRef, midRef string
	for _, e := range evs {
		if e.More > 100000 {
			bigRef = e.Ref
		} else if e.More > 0 {
			midRef = e.Ref
		}
	}
	if bigRef == "" || midRef == "" {
		t.Fatalf("no truncated events in %+v", evs)
	}

	var d eventReply
	if err := json.Unmarshal([]byte(body(t, r.a, eventPath(base, midRef), http.StatusOK)), &d); err != nil {
		t.Fatal(err)
	}
	if d.Text != mid || d.More != 0 {
		t.Errorf("mid event: %d bytes, more %d", len(d.Text), d.More)
	}
	if err := json.Unmarshal([]byte(body(t, r.a, eventPath(base, bigRef), http.StatusOK)), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Text) != runlive.MaxFullText || d.More != len(big)-runlive.MaxFullText || d.Limit != runlive.MaxFullText {
		t.Errorf("big event: %d bytes, more %d, limit %d", len(d.Text), d.More, d.Limit)
	}

	var poll struct{ HTML string }
	if err := json.Unmarshal([]byte(body(t, r.a, base+"/agent-log", http.StatusOK)), &poll); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "poll", poll.HTML, `data-ref="`+midRef+`"`, `class="xp"`, "more characters not shown")
	mustNotContain(t, "poll", poll.HTML, big[:5000])
	if len(poll.HTML) > 40<<10 {
		t.Errorf("poll is %d bytes", len(poll.HTML))
	}
}

func TestAgentEventRouteRejectsBadRefs(t *testing.T) {
	r := newLiveRig(t, "")
	base := "/runs/o/p/issues/live-7"
	for _, ref := range []string{
		"", "x", "t", "t1", "t1.", "t.0", "t-1.0", "t0.99", "t0.-1", "t5.0", "t999999999.0", "t0.0.0",
		"d1", "t../../etc/passwd", "t0.0/../x", "/etc/passwd", "t" + r.transcript, "../../../etc/passwd",
	} {
		body(t, r.a, eventPath(base, ref), http.StatusNotFound)
	}
	body(t, r.a, base+"/agent-log/event", http.StatusNotFound)
	body(t, r.a, eventPath("/runs/o/p/issues/live-8", "t0.0"), http.StatusNotFound)
	body(t, r.a, eventPath("/runs/o/p/issues/99", "t0.0"), http.StatusNotFound)
	body(t, r.a, eventPath(base, "t0.0"), http.StatusOK)
}

func TestAgentEventRouteDoesNotFollowASymlinkedTranscript(t *testing.T) {
	r := newLiveRig(t, "")
	secret := t.TempDir() + "/secret.jsonl"
	mustWrite(t, secret, `{"type":"user","message":{"role":"user","content":"secret"}}`+"\n")
	if err := os.Remove(r.transcript); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, r.transcript); err != nil {
		t.Fatal(err)
	}
	body(t, r.a, eventPath("/runs/o/p/issues/live-7", "t0.0"), http.StatusNotFound)
}

func TestAgentEventRouteIsHiddenWithTheAgentLog(t *testing.T) {
	r := newLiveRig(t, "dashboard:\n  hide:\n    sections: [agent_log]\n")
	body(t, r.a, eventPath("/runs/o/p/issues/live-7", "t0.0"), http.StatusNotFound)
	r = newLiveRig(t, "dashboard:\n  hide:\n    pages: [runs]\n")
	body(t, r.a, eventPath("/runs/o/p/issues/live-7", "t0.0"), http.StatusNotFound)
}

func TestStoredRunMarksEngineCutsAndExpandsRows(t *testing.T) {
	r := newLiveRig(t, "")
	long := strings.Repeat("l", runlive.MaxText+300)
	r.addRows(t,
		`INSERT INTO run_events VALUES ('D5', 3, '2026-01-01T10:00:03Z', 'notification', 'c', 1, '{"message":"`+strings.Repeat("c", 500)+`…"}')`,
		`INSERT INTO run_events VALUES ('D5', 4, '2026-01-01T10:00:04Z', 'notification', 'c', 1, '{"message":"`+long+`"}')`,
	)
	r.storeRuns(t, 5)
	base := "/runs/o/p/issues/5"
	page := body(t, r.a, base, http.StatusOK)
	mustContain(t, "stored run", page, "Truncated by the engine.", `data-ref="d4"`, "300 more characters not shown.")
	if strings.Count(page, `class="xp"`) != 1 {
		t.Errorf("expand buttons = %d, want 1 (the engine cut row has none)", strings.Count(page, `class="xp"`))
	}
	var d eventReply
	if err := json.Unmarshal([]byte(body(t, r.a, eventPath(base, "d4"), http.StatusOK)), &d); err != nil {
		t.Fatal(err)
	}
	if d.Text != long || d.More != 0 {
		t.Errorf("row text: %d bytes, more %d", len(d.Text), d.More)
	}
	for _, ref := range []string{"d99", "d", "dx", "d-1", "d1.0"} {
		body(t, r.a, eventPath(base, ref), http.StatusNotFound)
	}
}

func TestAgentEventRouteIsGetOnly(t *testing.T) {
	r := newLiveRig(t, "")
	h := r.a.routes(newHostGuard("127.0.0.1"))
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(m, eventPath("/runs/o/p/issues/live-7", "t0.0"), nil)
		req.Host = "127.0.0.1"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, rec.Code)
		}
	}
}
