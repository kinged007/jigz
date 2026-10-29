package gen

import (
	"sort"
	"strings"

	_ "embed"

	"github.com/kinged007/jigz/internal/config"
)

//go:embed skeleton.tmpl
var skeleton []byte

// MarkerPrefix starts the first line of a generated document. The line
// carries the digest of the file without it, so a hand edit is detected.
const MarkerPrefix = "# jig:generated v1"

// Loop is one engine process: one document, one candidate list and one
// query. A work entry with several candidate lists produces several
// loops.
type Loop struct {
	Name   string
	Entry  config.Resolved
	Agents []string
	// Agent is the name of the first candidate. agent-exec selects it.
	Agent string
	// Runtime and Model are the first candidate's, since one loop runs
	// one agent kind and one model.
	Runtime string
	Model   string
	Rules   []config.ResolvedRule
	Query   string
	Active  []string
}

// Split turns one resolved work entry into its loops. Rules with the
// same candidate list form one group and share a loop named after the
// slug; rules with different lists become loops named <slug>-<group>.
//
// The query of a loop that shares the entry with others is the entry's
// query_filter combined with the label clauses of its own rules, and its
// active_states are those labels plus the in-progress state. That
// narrowing is the only thing that keeps two loops of one entry from
// claiming the same item: the engine dispatches a query-matching item
// even when no rule matches it.
func Split(res config.Resolved, agents map[string]config.Agent) []Loop {
	groups := groupRules(res.Rules)
	if len(groups) == 0 {
		return []Loop{newLoop(res, res.Slug, res.Agents, nil, res.QueryFilter, nil, agents)}
	}
	many := len(groups) > 1
	out := make([]Loop, 0, len(groups))
	for _, g := range groups {
		name, query := res.Slug, res.QueryFilter
		var active []string
		if many {
			name = res.Slug + "-" + groupSuffix(g.rules)
			query = combine(res.QueryFilter, labelsOf(g.rules))
			active = activeStates(res, g.rules)
		}
		out = append(out, newLoop(res, name, g.agents, g.rules, query, active, agents))
	}
	return out
}

// ForAgent returns the loop set to run as the named active agent: the
// same candidate list, rules and query, but the agent's kind and model.
// The second result is false when the name is not a known agent. The
// supervisor uses it to rewrite a managed document at idle when the
// gate picks a standby agent; a restart is not needed, the engine
// re-reads the document live.
func (l Loop) ForAgent(agents map[string]config.Agent, name string) (Loop, bool) {
	a, ok := agents[name]
	if !ok {
		return l, false
	}
	out := l
	out.Agent = name
	out.Runtime = a.Runtime
	out.Model = a.Model
	for _, r := range l.Rules {
		if r.Model != "" {
			out.Model = r.Model
			break
		}
	}
	return out, true
}

func newLoop(res config.Resolved, name string, names []string, rules []config.ResolvedRule, query string, active []string, agents map[string]config.Agent) Loop {
	l := Loop{Name: name, Entry: res, Agents: names, Rules: rules, Query: query, Active: active}
	if len(names) > 0 {
		l.Agent = names[0]
		a := agents[l.Agent]
		l.Runtime = a.Runtime
		l.Model = a.Model
		for _, r := range rules {
			if r.Model != "" {
				l.Model = r.Model
				break
			}
		}
	}
	return l
}

type ruleGroup struct {
	agents []string
	rules  []config.ResolvedRule
}

// groupRules collects rules in order, keeping one group per distinct
// candidate list. The first rule that uses a list names the group.
func groupRules(rules []config.ResolvedRule) []ruleGroup {
	var groups []ruleGroup
	index := map[string]int{}
	for _, r := range rules {
		key := strings.Join(r.Agents, "\x00")
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, ruleGroup{agents: r.Agents})
		}
		groups[i].rules = append(groups[i].rules, r)
	}
	return groups
}

func groupSuffix(rules []config.ResolvedRule) string {
	if len(rules) == 0 || len(rules[0].Agents) == 0 {
		return "loop"
	}
	return rules[0].Agents[0]
}

func labelsOf(rules []config.ResolvedRule) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rules {
		for _, l := range r.Labels {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	sort.Strings(out)
	return out
}

// combine narrows a query to the given labels. The labels are the only
// filter the rules match on today, so the narrowed query is the base
// query plus one label term per label.
func combine(base string, labels []string) string {
	if len(labels) == 0 {
		return base
	}
	terms := make([]string, 0, len(labels))
	for _, l := range labels {
		terms = append(terms, "label:"+l)
	}
	return strings.TrimSpace(base + " " + strings.Join(terms, ","))
}

func activeStates(res config.Resolved, rules []config.ResolvedRule) []string {
	seen := map[string]bool{res.Tracker.InProgressState: true}
	var out []string
	if res.Tracker.InProgressState != "" {
		out = append(out, res.Tracker.InProgressState)
	}
	for _, l := range labelsOf(rules) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}
