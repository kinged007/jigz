package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Graph is the knowledge graph written to graph.json.
type Graph struct {
	Repo     RepoMeta `json:"repo"`
	SyncedAt string   `json:"syncedAt"`
	BuiltAt  string   `json:"builtAt"`
	Nodes    []*Node  `json:"nodes"`
	Links    []Link   `json:"links"`
	Workflow Workflow `json:"workflow"`
	Stats    Stats    `json:"stats"`
}

type Node struct {
	ID    string         `json:"id"`
	Type  string         `json:"type"`
	Label string         `json:"label"`
	Title string         `json:"title,omitempty"`
	T     int64          `json:"t,omitempty"` // first appearance, unix seconds
	State string         `json:"state,omitempty"`
	Deg   int            `json:"deg"`
	P     [3]float64     `json:"p"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// Link joins two node indexes.
type Link struct {
	S    int    `json:"s"`
	T    int    `json:"t"`
	Kind string `json:"k"`
	W    int    `json:"w,omitempty"`
}

type Stats struct {
	Counts    map[string]int `json:"counts"`
	Metrics   map[string]any `json:"metrics"`
	Labels    []LabelCount   `json:"labels"`
	Users     []UserCount    `json:"users"`
	Keywords  []Keyword      `json:"keywords"`
	Scopes    []Keyword      `json:"scopes"`
	Hotspots  []Hotspot      `json:"hotspots"`
	Days      []Day          `json:"days"` // one bucket per day from the first to the last active day
	Recent    []RecentEvent  `json:"recent"`
	FirstTime int64          `json:"firstTime"`
	LastTime  int64          `json:"lastTime"`
}

type LabelCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Stage bool   `json:"stage"`
}

type UserCount struct {
	Login    string `json:"login"`
	Items    int    `json:"items"`
	Commits  int    `json:"commits"`
	Comments int    `json:"comments"`
}

type Hotspot struct {
	Path    string `json:"path"`
	Commits int    `json:"commits"`
	Lines   int    `json:"lines"`
}

type Day struct {
	Start   int64 `json:"t"`
	Issues  int   `json:"issues"`
	PRs     int   `json:"prs"`
	Merged  int   `json:"merged"`
	Commits int   `json:"commits"`
	Events  int   `json:"events"`
}

type RecentEvent struct {
	At    int64  `json:"t"`
	Actor string `json:"actor"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	State string `json:"state,omitempty"`
	Node  string `json:"node"`
}

type builder struct {
	nodes []*Node
	idx   map[string]int
	links map[[2]int]*Link
	order []*Link
}

func (b *builder) node(id, typ, label string, t int64) *Node {
	if i, ok := b.idx[id]; ok {
		n := b.nodes[i]
		if t > 0 && (n.T == 0 || t < n.T) {
			n.T = t
		}
		return n
	}
	n := &Node{ID: id, Type: typ, Label: label, T: t}
	b.idx[id] = len(b.nodes)
	b.nodes = append(b.nodes, n)
	return n
}

// link adds an undirected edge; repeating it raises its weight.
func (b *builder) link(a, c, kind string) {
	i, ok1 := b.idx[a]
	j, ok2 := b.idx[c]
	if !ok1 || !ok2 || i == j {
		return
	}
	k := [2]int{i, j}
	if i > j {
		k = [2]int{j, i}
	}
	if l, ok := b.links[k]; ok {
		l.W++
		if kind == "closes" {
			l.Kind = kind
		}
		return
	}
	l := &Link{S: i, T: j, Kind: kind, W: 1}
	b.links[k] = l
	b.order = append(b.order, l)
}

func itemNodeID(kind string, n int) string { return fmt.Sprintf("%s:%d", kind, n) }

func loginFor(name, email string) string {
	if m := noreplyRe.FindStringSubmatch(strings.ToLower(email)); m != nil {
		return m[1]
	}
	return name
}

func buildGraph(st *Store, dataDir string) (*Graph, error) {
	g := &Graph{BuiltAt: time.Now().UTC().Format(time.RFC3339)}
	raw, _ := st.getMeta("repo")
	_ = json.Unmarshal([]byte(raw), &g.Repo)
	g.SyncedAt, _ = st.getMeta("synced_at")
	items, err := st.items("")
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no items stored; run sync first")
	}
	var commits []Commit
	headFiles := map[string]bool{}
	if _, err := os.Stat(filepath.Join(dataDir, "repo.git")); err == nil && g.Repo.DefaultBranch != "" {
		if commits, err = readCommits(dataDir, g.Repo.DefaultBranch); err != nil {
			return nil, err
		}
		if headFiles, err = listFiles(dataDir, g.Repo.DefaultBranch); err != nil {
			return nil, err
		}
	}

	b := &builder{idx: map[string]int{}, links: map[[2]int]*Link{}}
	repoLabel := g.Repo.NameWithOwner
	if repoLabel == "" {
		repoLabel = "repo"
	}
	b.node("repo", "repo", repoLabel, parseTime(g.Repo.CreatedAt))

	stages := stageLabels(items)
	paths := map[*Item][]Step{}
	kindOf := map[int]string{}
	for _, it := range items {
		paths[it] = itemPath(it, stages)
		kindOf[it.Number] = it.Kind
	}
	g.Workflow = inferWorkflow(items, stages, paths)
	for _, s := range g.Workflow.Stages {
		n := b.node(s.ID, "stage", s.Name, 0)
		n.Meta = map[string]any{"items": s.Items, "dwellH": s.MedianDwell, "order": s.Order, "synthetic": s.Synthetic}
	}
	for _, t := range g.Workflow.Transitions {
		for i := 0; i < t.Count; i++ {
			b.link(stageNodeID(t.From), stageNodeID(t.To), "flow")
		}
	}

	// Keywords over item titles and bodies.
	titles, bodies := make([]string, len(items)), make([]string, len(items))
	for i, it := range items {
		titles[i], bodies[i] = it.Title, it.Body
	}
	minDF := max(3, len(items)/500)
	vocab, top := keywordIndex(titles, bodies, minDF, 0.25, 220, 3)
	g.Stats.Keywords = vocab
	if len(g.Stats.Keywords) > 40 {
		g.Stats.Keywords = g.Stats.Keywords[:40]
	}

	userStats := map[string]*UserCount{}
	user := func(login string, t int64) string {
		if login == "" {
			login = "ghost"
		}
		b.node("user:"+login, "user", "@"+login, t)
		if userStats[login] == nil {
			userStats[login] = &UserCount{Login: login}
		}
		return "user:" + login
	}
	labelCounts := map[string]int{}
	scopeDF := map[string]int{}
	var recent []RecentEvent
	days := map[int64]*Day{}
	day := func(t int64) *Day {
		if t == 0 {
			return &Day{}
		}
		d := t - t%86400
		if days[d] == nil {
			days[d] = &Day{Start: d}
		}
		return days[d]
	}

	for i, it := range items {
		id := itemNodeID(it.Kind, it.Number)
		created := parseTime(it.CreatedAt)
		n := b.node(id, it.Kind, fmt.Sprintf("#%d", it.Number), created)
		n.Title, n.State = it.Title, it.State
		steps := paths[it]
		n.Meta = map[string]any{"author": it.Author, "labels": it.Labels, "comments": it.Comments,
			"path": steps, "keywords": top[i], "closedAt": parseTime(it.ClosedAt)}
		if it.Kind == "pr" {
			n.Meta["mergedAt"] = parseTime(it.MergedAt)
			n.Meta["head"] = it.HeadRef
			n.Meta["base"] = it.BaseRef
			n.Meta["add"] = it.Additions
			n.Meta["del"] = it.Deletions
		}
		if it.Milestone != "" {
			n.Meta["milestone"] = it.Milestone
		}
		if it.Kind == "issue" && closedByPR(it) {
			n.Meta["byPR"] = true
		}
		w := day(created)
		if it.Kind == "issue" {
			w.Issues++
		} else {
			w.PRs++
		}
		if it.MergedAt != "" {
			day(parseTime(it.MergedAt)).Merged++
		}
		b.link(id, user(it.Author, created), "authored")
		userStats[it.Author].Items++
		for _, a := range it.Assignees {
			b.link(id, user(a, created), "assigned")
		}
		for _, l := range it.Labels {
			labelCounts[l]++
			if stages[l] {
				continue
			}
			b.node("label:"+l, "label", l, created)
			b.link(id, "label:"+l, "label")
		}
		for _, s := range steps {
			b.link(id, stageNodeID(s.Stage), "stage")
		}
		for _, k := range top[i] {
			b.node("kw:"+k, "keyword", k, created)
			b.link(id, "kw:"+k, "topic")
		}
		_, scopes := conventional(it.Title)
		for _, s := range scopes {
			b.node("scope:"+s, "scope", s, created)
			b.link(id, "scope:"+s, "scope")
			scopeDF[s]++
		}
		if it.Milestone != "" {
			b.node("milestone:"+it.Milestone, "milestone", it.Milestone, created)
			b.link(id, "milestone:"+it.Milestone, "milestone")
		}
		var ev [][]any
		for _, e := range sortedEvents(it) {
			t := parseTime(e.At)
			day(t).Events++
			ev = append(ev, compactEvent(e, t))
			if e.Actor != "" && e.Type == "comment" {
				userStats[e.Actor] = orNew(userStats[e.Actor], e.Actor)
				userStats[e.Actor].Comments++
			}
			if e.Type != "comment" {
				recent = append(recent, RecentEvent{At: t, Actor: e.Actor, Type: e.Type, Label: e.Label, State: e.State, Node: id})
			}
		}
		n.Meta["events"] = ev
	}

	// Cross references need every item node to exist first.
	for _, it := range items {
		id := itemNodeID(it.Kind, it.Number)
		for _, c := range it.Closes {
			if k, ok := kindOf[c]; ok {
				b.link(id, itemNodeID(k, c), "closes")
			}
		}
		for _, e := range it.Events {
			k, ok := kindOf[e.Ref]
			if !ok || e.Ref == 0 {
				continue
			}
			kind := "mentions"
			switch {
			case e.Type == "xref" && e.Close, e.Type == "connected", e.Type == "closed":
				kind = "closes"
			case e.Type == "duplicate":
				kind = "duplicate"
			}
			b.link(itemNodeID(k, e.Ref), id, kind)
		}
		for _, e := range it.Events {
			if e.Actor != "" && (e.Type == "review" || e.Type == "comment") {
				b.link(id, user(e.Actor, parseTime(e.At)), "engaged")
			}
		}
	}

	// Code: commits, files, directories.
	mergeCommit := map[string]string{}
	for _, it := range items {
		if it.MergeCommit != "" {
			mergeCommit[it.MergeCommit] = itemNodeID(it.Kind, it.Number)
		}
	}
	churn := map[string]*Hotspot{}
	for _, c := range commits {
		id := "commit:" + c.Hash[:7]
		n := b.node(id, "commit", c.Hash[:7], c.At)
		n.Title = c.Subject
		adds, dels := 0, 0
		for _, f := range c.Files {
			adds += f.Add
			dels += f.Del
		}
		n.Meta = map[string]any{"author": c.Author, "add": adds, "del": dels, "files": len(c.Files)}
		day(c.At).Commits++
		login := loginFor(c.Author, c.Email)
		b.link(id, user(login, c.At), "committed")
		userStats[login].Commits++
		if pr, ok := mergeCommit[c.Hash]; ok {
			b.link(id, pr, "merged")
		}
		for _, r := range issueRefs(c.Subject) {
			if k, ok := kindOf[r]; ok {
				b.link(id, itemNodeID(k, r), "refs")
			}
		}
		_, scopes := conventional(c.Subject)
		for _, s := range scopes {
			b.node("scope:"+s, "scope", s, c.At)
			b.link(id, "scope:"+s, "scope")
		}
		for _, f := range c.Files {
			if !headFiles[f.Path] {
				continue
			}
			fileNode(b, f.Path, c.At)
			b.link(id, "file:"+f.Path, "touches")
			h := churn[f.Path]
			if h == nil {
				h = &Hotspot{Path: f.Path}
				churn[f.Path] = h
			}
			h.Commits++
			h.Lines += f.Add + f.Del
		}
	}
	for p := range headFiles {
		fileNode(b, p, 0)
	}
	for p, h := range churn {
		n := b.nodes[b.idx["file:"+p]]
		n.Meta = map[string]any{"commits": h.Commits, "lines": h.Lines}
	}

	// Stats.
	for _, n := range b.nodes {
		if n.T > 0 && (g.Stats.FirstTime == 0 || n.T < g.Stats.FirstTime) {
			g.Stats.FirstTime = n.T
		}
		if n.T > g.Stats.LastTime {
			g.Stats.LastTime = n.T
		}
	}
	g.Stats.Counts = map[string]int{}
	for _, n := range b.nodes {
		g.Stats.Counts[n.Type]++
	}
	for l, c := range labelCounts {
		g.Stats.Labels = append(g.Stats.Labels, LabelCount{l, c, stages[l]})
	}
	sort.Slice(g.Stats.Labels, func(i, j int) bool {
		if g.Stats.Labels[i].Count != g.Stats.Labels[j].Count {
			return g.Stats.Labels[i].Count > g.Stats.Labels[j].Count
		}
		return g.Stats.Labels[i].Name < g.Stats.Labels[j].Name
	})
	for _, u := range userStats {
		g.Stats.Users = append(g.Stats.Users, *u)
	}
	sort.Slice(g.Stats.Users, func(i, j int) bool {
		a, c := g.Stats.Users[i], g.Stats.Users[j]
		if a.Items+a.Commits+a.Comments != c.Items+c.Commits+c.Comments {
			return a.Items+a.Commits+a.Comments > c.Items+c.Commits+c.Comments
		}
		return a.Login < c.Login
	})
	for s, d := range scopeDF {
		g.Stats.Scopes = append(g.Stats.Scopes, Keyword{s, d})
	}
	sort.Slice(g.Stats.Scopes, func(i, j int) bool {
		if g.Stats.Scopes[i].DF != g.Stats.Scopes[j].DF {
			return g.Stats.Scopes[i].DF > g.Stats.Scopes[j].DF
		}
		return g.Stats.Scopes[i].Term < g.Stats.Scopes[j].Term
	})
	g.Stats.Scopes = head(g.Stats.Scopes, 30)
	for _, h := range churn {
		g.Stats.Hotspots = append(g.Stats.Hotspots, *h)
	}
	sort.Slice(g.Stats.Hotspots, func(i, j int) bool {
		if g.Stats.Hotspots[i].Commits != g.Stats.Hotspots[j].Commits {
			return g.Stats.Hotspots[i].Commits > g.Stats.Hotspots[j].Commits
		}
		return g.Stats.Hotspots[i].Path < g.Stats.Hotspots[j].Path
	})
	g.Stats.Hotspots = head(g.Stats.Hotspots, 40)
	var first, last int64
	for d := range days {
		if d > 0 && (first == 0 || d < first) {
			first = d
		}
		last = max(last, d)
	}
	for d := first; first > 0 && d <= last; d += 86400 {
		if days[d] != nil {
			g.Stats.Days = append(g.Stats.Days, *days[d])
		} else {
			g.Stats.Days = append(g.Stats.Days, Day{Start: d})
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].At > recent[j].At })
	g.Stats.Recent = head(recent, 80)
	g.Stats.Metrics = metrics(items, paths, len(commits))

	b.nodes[b.idx["repo"]].Meta = map[string]any{"description": g.Repo.Description, "stars": g.Repo.Stars}
	g.Nodes = b.nodes
	g.Links = make([]Link, len(b.order))
	for i, l := range b.order {
		g.Links[i] = *l
		b.nodes[l.S].Deg++
		b.nodes[l.T].Deg++
	}
	layout(g.Nodes, g.Links, 320)
	return g, nil
}

func orNew(u *UserCount, login string) *UserCount {
	if u == nil {
		return &UserCount{Login: login}
	}
	return u
}

func head[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// compactEvent encodes an event as [time, type, actor, label or ref].
func compactEvent(e Event, t int64) []any {
	out := []any{t, e.Type, e.Actor}
	switch {
	case e.Label != "":
		out = append(out, e.Label)
	case e.Ref != 0:
		out = append(out, e.Ref)
	case e.State != "":
		out = append(out, e.State)
	}
	return out
}

// fileNode adds a file node and its directory chain up to the repo node.
func fileNode(b *builder, p string, t int64) {
	if _, ok := b.idx["file:"+p]; ok {
		b.node("file:"+p, "file", path.Base(p), t)
		return
	}
	n := b.node("file:"+p, "file", path.Base(p), t)
	n.Title = p
	child := "file:" + p
	for dir := path.Dir(p); ; dir = path.Dir(dir) {
		if dir == "." || dir == "/" {
			b.link("repo", child, "contains")
			return
		}
		id := "dir:" + dir
		_, existed := b.idx[id]
		d := b.node(id, "dir", path.Base(dir)+"/", t)
		d.Title = dir + "/"
		b.link(id, child, "contains")
		if existed {
			return
		}
		child = id
	}
}

func metrics(items []*Item, paths map[*Item][]Step, commits int) map[string]any {
	var issueClose, prMerge, issueSteps []float64
	var issues, issuesOpen, issuesClosedByPR, prs, prsMerged, prsOpen, comments int
	for _, it := range items {
		comments += it.Comments
		if it.Kind == "issue" {
			issues++
			if it.State == "OPEN" {
				issuesOpen++
			}
			if it.ClosedAt != "" && it.State == "CLOSED" {
				issueClose = append(issueClose, float64(parseTime(it.ClosedAt)-parseTime(it.CreatedAt))/3600)
			}
			issueSteps = append(issueSteps, float64(len(paths[it])))
			if closedByPR(it) {
				issuesClosedByPR++
			}
			continue
		}
		prs++
		switch it.State {
		case "MERGED":
			prsMerged++
			prMerge = append(prMerge, float64(parseTime(it.MergedAt)-parseTime(it.CreatedAt))/3600)
		case "OPEN":
			prsOpen++
		}
	}
	pct := func(a, c int) float64 {
		if c == 0 {
			return 0
		}
		return round1(100 * float64(a) / float64(c))
	}
	return map[string]any{
		"issues": issues, "issuesOpen": issuesOpen, "prs": prs, "prsMerged": prsMerged, "prsOpen": prsOpen,
		"commits": commits, "comments": comments,
		"issueCloseMedianH": round1(median(issueClose)), "prMergeMedianH": round1(median(prMerge)),
		"medianStages": median(issueSteps), "mergeRate": pct(prsMerged, prs),
		"issuesLinkedPct": pct(issuesClosedByPR, issues),
	}
}

// closedByPR reports whether a pull request closed or will close the issue.
func closedByPR(it *Item) bool {
	for _, e := range it.Events {
		if (e.Type == "closed" && e.Ref != 0) || (e.Type == "xref" && e.Close) {
			return true
		}
	}
	return false
}
