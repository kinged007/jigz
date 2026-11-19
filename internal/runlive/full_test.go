package runlive

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestClipCountsTheCutCharacters(t *testing.T) {
	e := newEvent("", KindMessage, "t", strings.Repeat("é", MaxText), "", false)
	if !e.Truncated || e.More != MaxText-len(e.Text)/2 {
		t.Errorf("More = %d, kept %d bytes", e.More, len(e.Text))
	}
	if e := newEvent("", KindMessage, "t", "short", "", false); e.Truncated || e.More != 0 {
		t.Errorf("short text: %+v", e)
	}
}

func TestTranscriptEventsHaveRefsAndFullText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	long := strings.Repeat("ab", MaxText)
	write(t, p, `{"type":"user","timestamp":"t","message":{"role":"user","content":"first"}}`+"\n"+
		`{"type":"assistant","timestamp":"t","message":{"role":"assistant","content":[{"type":"text","text":"x"},{"type":"text","text":"`+long+`"}]}}`+"\n")
	evs, err := TailEvents(p, Claude, 10)
	if err != nil || len(evs) != 3 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	if evs[0].Ref != "t0.0" || evs[2].Ref == "" || evs[1].Ref == evs[2].Ref {
		t.Errorf("refs = %q %q %q", evs[0].Ref, evs[1].Ref, evs[2].Ref)
	}
	if !evs[2].Truncated || evs[1].Truncated {
		t.Errorf("truncated flags: %v %v", evs[1].Truncated, evs[2].Truncated)
	}
	var off int64
	var sub int
	if _, err := fmt.Sscanf(evs[2].Ref, "t%d.%d", &off, &sub); err != nil {
		t.Fatal(err)
	}
	text, more, err := EventText(p, Claude, off, sub)
	if err != nil || text != long || more != 0 {
		t.Errorf("EventText = %d bytes, more %d, %v", len(text), more, err)
	}
	for _, c := range []struct {
		off int64
		sub int
	}{{-1, 0}, {5, 0}, {off, 9}, {off, -1}, {1 << 40, 0}} {
		if _, _, err := EventText(p, Claude, c.off, c.sub); err == nil {
			t.Errorf("EventText(%d, %d) succeeded", c.off, c.sub)
		}
	}
	if _, _, err := EventText(p, "other", 0, 0); err == nil {
		t.Error("an unknown kind was read")
	}
}

func TestEventTextIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p, `{"type":"user","timestamp":"t","message":{"role":"user","content":"`+strings.Repeat("x", MaxFullText+500)+`"}}`+"\n")
	text, more, err := EventText(p, Claude, 0, 0)
	if err != nil || len(text) != MaxFullText || more != 500 {
		t.Errorf("EventText = %d bytes, more %d, %v", len(text), more, err)
	}
	write(t, p, `{"type":"user","timestamp":"t","message":{"role":"user","content":"`+strings.Repeat("x", maxLineBytes)+`"}}`+"\n")
	if _, _, err := EventText(p, Claude, 0, 0); err == nil {
		t.Error("a line over the line cap was read")
	}
}

func TestEngineRowsMarkEngineCutsAndGiveFullText(t *testing.T) {
	path := engineDB(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cut := strings.Repeat("c", 200) + "…"
	long := strings.Repeat("l", MaxText+10)
	for _, q := range []string{
		`INSERT INTO run_events VALUES ('D1', 4, 't4', 'notification', 'c', 1, '{"message":"` + cut + `"}')`,
		`INSERT INTO run_events VALUES ('D1', 5, 't5', 'notification', 'c', 1, '{"message":"` + long + `"}')`,
		`INSERT INTO run_events VALUES ('D1', 6, 't6', 'notification', 'c', 1, '{"message":"ends with an ellipsis…"}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := ReadRunEvents(path, "D1")
	if err != nil || len(evs) != 6 {
		t.Fatalf("events = %d, %v", len(evs), err)
	}
	if evs[0].Ref != "d1" || !evs[3].EngineCut || evs[3].Truncated {
		t.Errorf("engine cut row: %+v", evs[3])
	}
	if evs[5].EngineCut || evs[0].EngineCut {
		t.Error("a short message was marked as cut by the engine")
	}
	if !evs[4].Truncated || evs[4].More != 10 || evs[4].EngineCut {
		t.Errorf("long row: truncated=%v more=%d", evs[4].Truncated, evs[4].More)
	}
	text, more, err := ReadRowText(path, "D1", 5)
	if err != nil || text != long || more != 0 {
		t.Errorf("ReadRowText = %d bytes, more %d, %v", len(text), more, err)
	}
	if _, _, err := ReadRowText(path, "D2", 5); err == nil {
		t.Error("a row of another dispatch was read")
	}
}
