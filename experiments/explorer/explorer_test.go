package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func ev(typ, at, label string) Event { return Event{Type: typ, At: at, Label: label} }

func testItems() []*Item {
	// build/review swap labels like an automation; type:bug stays on.
	mk := func(n int, state string, events ...Event) *Item {
		return &Item{Number: n, Kind: "issue", State: state, CreatedAt: "2026-01-01T00:00:00Z",
			ClosedAt: "2026-01-02T00:00:00Z", Events: events}
	}
	var items []*Item
	for n := 1; n <= 6; n++ {
		items = append(items, mk(n, "CLOSED",
			ev("labeled", "2026-01-01T00:00:01Z", "type:bug"),
			ev("labeled", "2026-01-01T01:00:00Z", "build"),
			ev("unlabeled", "2026-01-01T03:00:00Z", "build"),
			ev("labeled", "2026-01-01T03:00:00Z", "review"),
			ev("unlabeled", "2026-01-01T04:00:00Z", "review"),
		))
	}
	items = append(items, mk(7, "OPEN", ev("labeled", "2026-01-01T01:00:00Z", "type:bug")))
	return items
}

func TestStageLabelsSeparatesTransientLabels(t *testing.T) {
	got := stageLabels(testItems())
	want := map[string]bool{"build": true, "review": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
}

func TestItemPathAndWorkflow(t *testing.T) {
	items := testItems()
	stages := stageLabels(items)
	paths := map[*Item][]Step{}
	for _, it := range items {
		paths[it] = itemPath(it, stages)
	}
	var names []string
	for _, s := range paths[items[0]] {
		names = append(names, s.Stage)
	}
	if want := []string{"opened", "build", "review", "closed"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("path = %v, want %v", names, want)
	}
	if got := paths[items[6]]; len(got) != 1 || got[0].Stage != "opened" {
		t.Fatalf("open item path = %v", got)
	}

	wf := inferWorkflow(items, stages, paths)
	if wf.Variants[0].Count != 6 || len(wf.Variants[0].Path) != 4 {
		t.Fatalf("top variant = %+v", wf.Variants[0])
	}
	var buildDwell float64
	for _, s := range wf.Stages {
		if s.Name == "build" {
			buildDwell = s.MedianDwell
		}
	}
	if buildDwell != 2 {
		t.Fatalf("build dwell = %v h, want 2", buildDwell)
	}
	found := false
	for _, tr := range wf.Transitions {
		if tr.From == "build" && tr.To == "review" {
			found = tr.Count == 6 && tr.MedianH == 2
		}
	}
	if !found {
		t.Fatalf("build→review transition missing or wrong: %+v", wf.Transitions)
	}
}

func TestConventionalAndRefs(t *testing.T) {
	typ, scopes := conventional("fix(merge-queue, CI)!: check 4 reads the legacy API (#1840)")
	if typ != "fix" || !reflect.DeepEqual(scopes, []string{"merge-queue", "ci"}) {
		t.Fatalf("conventional = %q %v", typ, scopes)
	}
	if typ, _ := conventional("Dev server crashes"); typ != "" {
		t.Fatalf("plain title parsed as %q", typ)
	}
	if got := issueRefs("feat: x (#12), closes #7; not a/b#9 or #x"); !reflect.DeepEqual(got, []int{12, 7}) {
		t.Fatalf("refs = %v", got)
	}
}

func TestKeywordIndex(t *testing.T) {
	titles := []string{"oauth login fails", "oauth callback broken", "oauth token refresh", "billing invoice wrong", "billing plan change", "the the the"}
	bodies := make([]string, len(titles))
	bodies[0] = "```go\nsecretcode()\n``` see https://example.com/oauth"
	vocab, top := keywordIndex(titles, bodies, 2, 0.6, 10, 2)
	var terms []string
	for _, k := range vocab {
		terms = append(terms, k.Term)
	}
	if !reflect.DeepEqual(terms, []string{"oauth", "billing"}) {
		t.Fatalf("vocab = %v", terms)
	}
	if !reflect.DeepEqual(top[0], []string{"oauth"}) || len(top[5]) != 0 {
		t.Fatalf("top = %v", top)
	}
}

func TestParseGitLog(t *testing.T) {
	out := []byte("\x1eabc1234567\x1fAda\x1f1+ada@users.noreply.github.com\x1f1700000000\x1ffeat(api): add x (#5)\n\n" +
		"3\t1\tsrc/a.go\n-\t-\timg.png\n2\t0\tsrc/{old => new}/b.go\n" +
		"\x1edef7654321\x1fBob\x1fbob@example.com\x1f1700000100\x1fdocs: y\n\n1\t1\tREADME.md\n")
	cs := parseGitLog(out)
	if len(cs) != 2 || cs[0].Subject != "feat(api): add x (#5)" || cs[1].At != 1700000100 {
		t.Fatalf("commits = %+v", cs)
	}
	if got := cs[0].Files[2].Path; got != "src/new/b.go" {
		t.Fatalf("rename path = %q", got)
	}
	if loginFor(cs[0].Author, cs[0].Email) != "ada" || loginFor(cs[1].Author, cs[1].Email) != "Bob" {
		t.Fatal("loginFor did not map the noreply address")
	}
}

func TestStoreSkipsUnchangedItems(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	it := &Item{Number: 3, Kind: "pr", State: "MERGED", UpdatedAt: "2026-01-01T00:00:00Z", TimelineCursor: "c1"}
	if err := st.putItem(it, "now"); err != nil {
		t.Fatal(err)
	}
	if u, complete, ok, _ := st.storedUpdatedAt("pr", 3); !ok || complete || u != it.UpdatedAt {
		t.Fatalf("stored = %q complete=%v ok=%v", u, complete, ok)
	}
	it.TimelineCursor = ""
	_ = st.putItem(it, "now")
	if _, complete, _, _ := st.storedUpdatedAt("pr", 3); !complete {
		t.Fatal("timeline should be complete")
	}
	if _, _, ok, _ := st.storedUpdatedAt("issue", 3); ok {
		t.Fatal("issue 3 should not exist")
	}
}

func TestLayoutIsDeterministicAndFinite(t *testing.T) {
	mk := func() ([]*Node, []Link) {
		nodes := make([]*Node, 60)
		var links []Link
		for i := range nodes {
			nodes[i] = &Node{ID: string(rune('a' + i%26))}
			if i > 0 {
				links = append(links, Link{S: i, T: i / 3, Kind: "contains"})
			}
		}
		return nodes, links
	}
	a, la := mk()
	b, lb := mk()
	layout(a, la, 60)
	layout(b, lb, 60)
	for i := range a {
		if a[i].P != b[i].P {
			t.Fatalf("node %d: %v != %v", i, a[i].P, b[i].P)
		}
		for _, v := range a[i].P {
			if v != v || v > 1e6 || v < -1e6 {
				t.Fatalf("node %d has bad position %v", i, a[i].P)
			}
		}
	}
}

func TestClosedByPR(t *testing.T) {
	yes := &Item{Events: []Event{{Type: "closed", Ref: 12}}}
	viaXref := &Item{Events: []Event{{Type: "xref", Ref: 3, Close: true}}}
	no := &Item{Events: []Event{{Type: "closed"}, {Type: "xref", Ref: 3}}}
	if !closedByPR(yes) || !closedByPR(viaXref) || closedByPR(no) {
		t.Fatal("closedByPR mismatch")
	}
}

func TestLinkKindUpgradesToCloses(t *testing.T) {
	b := &builder{idx: map[string]int{}, links: map[[2]int]*Link{}}
	b.node("a", "issue", "a", 0)
	b.node("b", "pr", "b", 0)
	b.link("a", "b", "mentions")
	b.link("b", "a", "closes")
	if len(b.order) != 1 || b.order[0].Kind != "closes" || b.order[0].W != 2 {
		t.Fatalf("link = %+v", b.order[0])
	}
}

func getStatus(t *testing.T, dir string, r *refresher) Status {
	t.Helper()
	rec := httptest.NewRecorder()
	newMux(dir, r).ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var s Status
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStatus(t *testing.T) {
	dir := t.TempDir()
	if s := getStatus(t, dir, nil); s.Refresh != 0 || s.Version != "" {
		t.Fatalf("no refresher, no graph: %+v", s)
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s1 := getStatus(t, dir, nil)
	if s1.Refresh != 0 || s1.Version == "" {
		t.Fatalf("no refresher: %+v", s1)
	}

	r := &refresher{every: 90 * time.Second, nextAt: time.Now().Add(30 * time.Second)}
	s := getStatus(t, dir, r)
	if s.Refresh != 90 || s.NextIn < 29 || s.NextIn > 30 || s.Syncing {
		t.Fatalf("idle refresher: %+v", s)
	}
	r.set(true, "")
	if s := getStatus(t, dir, r); !s.Syncing || s.NextIn != 0 {
		t.Fatalf("syncing refresher: %+v", s)
	}
	r.set(false, "rate limited")
	if s := getStatus(t, dir, r); s.Error != "rate limited" {
		t.Fatalf("failed refresher: %+v", s)
	}

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "graph.json"), future, future); err != nil {
		t.Fatal(err)
	}
	if s2 := getStatus(t, dir, nil); s2.Version == s1.Version {
		t.Fatal("version did not change after graph.json was rewritten")
	}
}
