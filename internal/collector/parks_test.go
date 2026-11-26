package collector

import (
	"context"
	"testing"

	"github.com/kinged007/jigz/internal/store"
)

func TestNonRetryableFailureParksOnceWithOneEvent(t *testing.T) {
	path, db := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	if parks, _ := st.ActiveParks(); len(parks) != 0 {
		t.Fatalf("parks = %+v, want none on the fixture alone", parks)
	}
	if _, err := db.Exec(`INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace,
		started_at, completed_at, status, error, display_identifier) VALUES
		('21','21',1,'claude-code','','2026-10-15T00:02:00Z','2026-10-15T00:03:00Z','failed','{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}','acme/app#21'),
		('22','22',1,'claude-code','','2026-10-15T00:02:00Z','2026-10-15T00:03:00Z','succeeded','','acme/app#22')`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	park, found, err := st.ActivePark("acme/app", "build", "21")
	if err != nil || !found {
		t.Fatalf("park for 21 = %+v, %v, %v, want an active park", park, found, err)
	}
	if park.Class != "agent-unauthenticated" || park.Fix == "" {
		t.Errorf("park = %+v, want the class and a one-line fix", park)
	}
	if _, found, _ := st.ActivePark("acme/app", "build", "22"); found {
		t.Error("the succeeded issue 22 was parked")
	}
	events := func(issue string) []store.Event {
		ev, _ := st.IssueEvents("acme/app", "build", issue)
		var out []store.Event
		for _, e := range ev {
			if e.Kind == store.ParkEventKind {
				out = append(out, e)
			}
		}
		return out
	}
	if ev := events("21"); len(ev) != 1 {
		t.Fatalf("park events for 21 = %d, want exactly one", len(ev))
	} else if ev[0].Detail != park.Class+": "+park.Fix {
		t.Errorf("event detail = %q, want the same class and fix as the park", ev[0].Detail)
	}
	// The loop keeps processing: both new runs were copied.
	all, _ := st.Runs(store.RunFilter{Limit: store.MaxLimit})
	if len(all) != 18 {
		t.Errorf("runs = %d, want 18: the parked issue must not stop the loop", len(all))
	}
	// A second pass adds no second notice.
	c.Once(context.Background())
	if ev := events("21"); len(ev) != 1 {
		t.Errorf("park events for 21 after a repeat pass = %d, want one", len(ev))
	}
}

func TestRetryableFailuresParkNothing(t *testing.T) {
	path, db := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	if _, err := db.Exec(`INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace,
		started_at, completed_at, status, error, display_identifier) VALUES
		('23','23',1,'codex','','2026-10-15T00:02:00Z','2026-10-15T00:03:00Z','failed','error: usage limit reached, resets in 3h','acme/app#23'),
		('24','24',1,'codex','','2026-10-15T00:02:00Z','2026-10-15T00:03:00Z','failed','connection reset by peer','acme/app#24'),
		('25','25',1,'codex','','2026-10-15T00:02:00Z','2026-10-15T00:03:00Z','cancelled','Invalid API key','acme/app#25')`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	if parks, _ := st.ActiveParks(); len(parks) != 0 {
		t.Errorf("parks = %+v, want none for retryable and cancelled runs", parks)
	}
}
