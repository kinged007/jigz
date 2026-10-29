package gen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/kinged007/jigz/internal/config"
)

// data is the template's view of one loop.
type data struct {
	Skeleton            string
	Profile             Profile
	Digest              string
	LoopName            string
	DBPath              string
	QueryFilter         string
	Tracker             trackerView
	Hooks               hooksView
	PollingIntervalMS   int
	AgentKind           string
	AgentCommand        string
	MaxTurns            int
	MaxConcurrentAgents int
	RuntimeBlockName    string
	Model               string
	Rules               []ruleView
	DefaultPrompt       string
}

type trackerView struct {
	Kind            string
	ApiKey          string
	Project         string
	QueryFilter     string
	ActiveStates    []string
	InProgressState string
	HandoffState    string
}

type hooksView struct {
	AfterCreate string
	BeforeRun   string
	AfterRun    string
	TimeoutMS   int
}

type ruleView struct {
	Name   string
	Labels []string
	Prompt string
	Next   string
	Stage  string
}

// Render writes the document for one loop. The result carries the
// generated marker as its first front-matter line, and the digest covers
// the document with that line removed.
//
// The executable is the literal absolute path of jig plus the agent
// name. The engine does not expand $VAR in agent.command and does not
// preserve shell quoting, so no substitution and no metacharacters.
func Render(p Profile, l Loop, agents map[string]config.Agent, jigPath, defaultPrompt string) ([]byte, error) {
	if err := Validate(l, agents); err != nil {
		return nil, err
	}
	if jigPath == "" {
		return nil, errors.New("gen: empty jig path")
	}
	kind := agentKind(l)
	if kind == "" {
		return nil, fmt.Errorf("gen: loop %s has no agent kind", l.Name)
	}
	if !p.SupportsAgent(kind) {
		return nil, fmt.Errorf("gen: loop %s uses agent kind %q, which %s does not support", l.Name, kind, p.EngineVersion())
	}
	t := trackerOf(l)
	if !p.SupportsTracker(t.Kind) {
		return nil, fmt.Errorf("gen: loop %s uses tracker kind %q, which %s does not support", l.Name, t.Kind, p.EngineVersion())
	}

	d := data{
		Skeleton:            skeletonName(l),
		Profile:             p,
		LoopName:            l.Name,
		DBPath:              "jigz-" + l.Name + ".db",
		QueryFilter:         l.Query,
		Tracker:             t,
		Hooks:               hooksOf(l),
		PollingIntervalMS:   polling(l),
		AgentKind:           kind,
		AgentCommand:        strings.TrimSpace(jigPath) + " agent-exec " + agentName(l),
		MaxTurns:            maxTurns(l),
		MaxConcurrentAgents: maxConcurrent(l),
		RuntimeBlockName:    kind,
		Model:               modelOf(l),
		Rules:               rulesOf(l),
		DefaultPrompt:       defaultPrompt,
	}

	body, err := renderBody(d)
	if err != nil {
		return nil, err
	}
	d.Digest = digest(body)
	return renderBody(d)
}

// agentKind, agentName, modelOf and the loop-level numeric settings come
// from the loop's first candidate: one loop is one kind, one model and
// one set of runtime values. Tracker and hooks are the entry's resolved
// defaults; per-loop overrides stay in Resolved already.

func agentKind(l Loop) string { return l.Runtime }

func agentName(l Loop) string { return l.Agent }

func modelOf(l Loop) string { return l.Model }

func trackerOf(l Loop) trackerView {
	t := l.Entry.Tracker
	return trackerView{
		Kind:            t.Kind,
		ApiKey:          stringOf(t.ApiKey),
		Project:         stringOf(t.Project),
		QueryFilter:     l.Query,
		ActiveStates:    activeFor(l),
		InProgressState: t.InProgressState,
		HandoffState:    t.HandoffState,
	}
}

// activeFor is the loop's active_states: the split narrowing when the
// entry has several loops, the configured states when set, otherwise
// the rule labels plus the in-progress state. The engine rejects a
// document whose in-progress state is not an active state, so an entry
// that sets none gets the derivation rather than an empty list.
func activeFor(l Loop) []string {
	if l.Active != nil {
		return l.Active
	}
	if len(l.Entry.Tracker.ActiveStates) > 0 {
		return l.Entry.Tracker.ActiveStates
	}
	return activeStates(l.Entry, l.Rules)
}

// stringOf renders a StringOrRef the way the engine document needs it:
// a literal stays literal, a {from_env: NAME} reference becomes $NAME
// so the engine passes the supervisor's environment value through.
func stringOf(v config.StringOrRef) string {
	if v.FromEnv != "" {
		return "$" + v.FromEnv
	}
	return v.Str
}

func hooksOf(l Loop) hooksView {
	h := l.Entry.Hooks
	hv := hooksView{AfterCreate: h.AfterCreate, BeforeRun: h.BeforeRun, AfterRun: h.AfterRun, TimeoutMS: h.TimeoutMS}
	defaultHooks(l.Entry.Tracker.Kind, &hv)
	return hv
}

func polling(l Loop) int {
	if l.Entry.PollingIntervalMS != 0 {
		return l.Entry.PollingIntervalMS
	}
	return 60000
}

func maxTurns(l Loop) int {
	if l.Entry.MaxTurns != 0 {
		return l.Entry.MaxTurns
	}
	return 10
}

func maxConcurrent(l Loop) int {
	if l.Entry.MaxConcurrentAgents != 0 {
		return l.Entry.MaxConcurrentAgents
	}
	return 1
}

// Validate checks a loop before rendering: the first candidate must
// resolve to a known agent, and a next that leaves the loop's rules is
// a hand-off by label — engine stage chains live in one document — so
// generation refuses it and validation reports the prompt edit the
// author must write instead.
func Validate(l Loop, agents map[string]config.Agent) error {
	if len(l.Agents) > 0 {
		if _, ok := agents[l.Agents[0]]; !ok {
			return fmt.Errorf("gen: loop %s uses unknown agent %q", l.Name, l.Agents[0])
		}
	}
	for _, r := range l.Rules {
		if r.Next != "" && !ruleNamed(l.Rules, r.Next) {
			return fmt.Errorf("gen: rule %s of loop %s chains to %q, which lives in another loop; hand the item over by replacing the label with the next trigger instead", r.Name, l.Name, r.Next)
		}
	}
	return nil
}

func rulesOf(l Loop) []ruleView {
	rules := make([]ruleView, 0, len(l.Rules))
	for _, r := range l.Rules {
		rules = append(rules, ruleView{Name: r.Name, Labels: r.Labels, Prompt: r.Prompt, Next: r.Next, Stage: r.Stage})
	}
	return rules
}

func ruleNamed(rules []config.ResolvedRule, name string) bool {
	for _, r := range rules {
		if r.Name == name {
			return true
		}
	}
	return false
}

func skeletonName(l Loop) string { return l.Name }

func renderBody(d data) ([]byte, error) {
	t, err := template.New("workflow").Funcs(template.FuncMap{
		"indent": func(s string, n int) string {
			pad := strings.Repeat(" ", n)
			lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
			for i, l := range lines {
				lines[i] = pad + l
			}
			return strings.Join(lines, "\n")
		},
	}).Parse(string(skeleton))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// digest is the sha256 of the document with the marker line removed, so
// a hand edit to any other line changes it.
func digest(body []byte) string {
	lines := bytes.Split(body, []byte("\n"))
	kept := lines[:0]
	for _, l := range lines {
		if bytes.HasPrefix(bytes.TrimSpace(l), []byte(MarkerPrefix)) {
			continue
		}
		kept = append(kept, l)
	}
	sum := sha256.Sum256(bytes.Join(kept, []byte("\n")))
	return hex.EncodeToString(sum[:])
}
