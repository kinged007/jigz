package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Workflow is the process inferred from label timelines. No workflow is
// configured: a label counts as a stage when items regularly lose it again
// (an automation hands the item on by swapping labels). Labels that stay on
// an item (type, priority, area) are attributes.
type Workflow struct {
	Stages      []Stage      `json:"stages"`
	Transitions []Transition `json:"transitions"`
	Variants    []Variant    `json:"variants"`
}

type Stage struct {
	ID          string  `json:"id"` // node id
	Name        string  `json:"name"`
	Items       int     `json:"items"`
	Adds        int     `json:"adds"`
	Removes     int     `json:"removes"`
	MedianDwell float64 `json:"medianDwellH"` // hours between label add and removal
	Order       float64 `json:"order"`        // mean relative position in paths, 0..1
	Synthetic   bool    `json:"synthetic,omitempty"`
}

type Transition struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Count   int     `json:"count"`
	MedianH float64 `json:"medianH"`
}

type Variant struct {
	Path  []string `json:"path"`
	Count int      `json:"count"`
}

// Step is one stage an item entered, at unix time At.
type Step struct {
	Stage string `json:"s"`
	At    int64  `json:"t"`
}

const (
	stageOpened    = "opened"
	stageMerged    = "merged"
	stageClosed    = "closed"
	stageNotDone   = "not planned"
	minStageAdds   = 5
	minRemoveRatio = 0.35
)

func parseTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// stageLabels returns the labels that behave like workflow stages.
func stageLabels(items []*Item) map[string]bool {
	adds, removes := map[string]int{}, map[string]int{}
	for _, it := range items {
		for _, e := range it.Events {
			switch e.Type {
			case "labeled":
				adds[e.Label]++
			case "unlabeled":
				removes[e.Label]++
			}
		}
	}
	out := map[string]bool{}
	for l, a := range adds {
		if a >= minStageAdds && float64(removes[l]) >= minRemoveRatio*float64(a) {
			out[l] = true
		}
	}
	return out
}

func sortedEvents(it *Item) []Event {
	ev := append([]Event(nil), it.Events...)
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].At < ev[j].At })
	return ev
}

// itemPath is the sequence of stages an item went through: opened, each
// stage label in the order it was added (repeats collapsed), then the outcome.
func itemPath(it *Item, stages map[string]bool) []Step {
	path := []Step{{stageOpened, parseTime(it.CreatedAt)}}
	for _, e := range sortedEvents(it) {
		if e.Type == "labeled" && stages[e.Label] && path[len(path)-1].Stage != e.Label {
			path = append(path, Step{e.Label, parseTime(e.At)})
		}
	}
	switch {
	case it.State == "MERGED":
		path = append(path, Step{stageMerged, parseTime(it.MergedAt)})
	case it.State == "CLOSED" && (it.StateReason == "NOT_PLANNED" || it.StateReason == "DUPLICATE" || it.Kind == "pr"):
		path = append(path, Step{stageNotDone, parseTime(it.ClosedAt)})
	case it.State == "CLOSED":
		path = append(path, Step{stageClosed, parseTime(it.ClosedAt)})
	}
	return path
}

func stageNodeID(name string) string {
	switch name {
	case stageOpened, stageMerged, stageClosed, stageNotDone:
		return "stage:" + name
	}
	return "label:" + name
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

func inferWorkflow(items []*Item, stages map[string]bool, paths map[*Item][]Step) Workflow {
	type agg struct {
		items, adds, removes int
		dwell                []float64
		pos                  []float64
	}
	st := map[string]*agg{}
	get := func(n string) *agg {
		if st[n] == nil {
			st[n] = &agg{}
		}
		return st[n]
	}
	type tkey struct{ from, to string }
	trans := map[tkey][]float64{}
	variants := map[string]int{}

	for _, it := range items {
		path := paths[it]
		seen := map[string]bool{}
		for i, s := range path {
			a := get(s.Stage)
			if !seen[s.Stage] {
				a.items++
				seen[s.Stage] = true
			}
			if len(path) > 1 {
				a.pos = append(a.pos, float64(i)/float64(len(path)-1))
			}
			if i > 0 {
				k := tkey{path[i-1].Stage, s.Stage}
				trans[k] = append(trans[k], float64(s.At-path[i-1].At)/3600)
			}
		}
		names := make([]string, len(path))
		for i, s := range path {
			names[i] = s.Stage
		}
		variants[strings.Join(names, "\x00")]++

		added := map[string]int64{}
		for _, e := range sortedEvents(it) {
			if !stages[e.Label] {
				continue
			}
			switch e.Type {
			case "labeled":
				get(e.Label).adds++
				added[e.Label] = parseTime(e.At)
			case "unlabeled":
				get(e.Label).removes++
				if t, ok := added[e.Label]; ok {
					get(e.Label).dwell = append(get(e.Label).dwell, float64(parseTime(e.At)-t)/3600)
					delete(added, e.Label)
				}
			}
		}
	}

	var wf Workflow
	for name, a := range st {
		order := median(a.pos)
		synthetic := strings.HasPrefix(stageNodeID(name), "stage:")
		if name == stageOpened {
			order = 0
		} else if synthetic {
			order = 1
		}
		wf.Stages = append(wf.Stages, Stage{ID: stageNodeID(name), Name: name, Items: a.items, Adds: a.adds,
			Removes: a.removes, MedianDwell: round1(median(a.dwell)), Order: math.Round(order*100) / 100, Synthetic: synthetic})
	}
	sort.Slice(wf.Stages, func(i, j int) bool {
		if wf.Stages[i].Order != wf.Stages[j].Order {
			return wf.Stages[i].Order < wf.Stages[j].Order
		}
		return wf.Stages[i].Items > wf.Stages[j].Items
	})
	for k, d := range trans {
		wf.Transitions = append(wf.Transitions, Transition{From: k.from, To: k.to, Count: len(d), MedianH: round1(median(d))})
	}
	sort.Slice(wf.Transitions, func(i, j int) bool {
		if wf.Transitions[i].Count != wf.Transitions[j].Count {
			return wf.Transitions[i].Count > wf.Transitions[j].Count
		}
		return wf.Transitions[i].From+wf.Transitions[i].To < wf.Transitions[j].From+wf.Transitions[j].To
	})
	for p, c := range variants {
		wf.Variants = append(wf.Variants, Variant{Path: strings.Split(p, "\x00"), Count: c})
	}
	sort.Slice(wf.Variants, func(i, j int) bool {
		if wf.Variants[i].Count != wf.Variants[j].Count {
			return wf.Variants[i].Count > wf.Variants[j].Count
		}
		return len(wf.Variants[i].Path) < len(wf.Variants[j].Path)
	})
	if len(wf.Variants) > 25 {
		wf.Variants = wf.Variants[:25]
	}
	return wf
}
