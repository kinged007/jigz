package plan

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is one entry of dispatch.rules in a workflow file.
type Rule struct {
	Name     string
	Labels   []string
	Agent    string
	Template string
	Stage    string
	Next     string
}

// Workflow is the part of a workflow file's front matter that describes
// where it sits in a pipeline and how it runs.
type Workflow struct {
	TrackerKind    string
	QueryFilter    string
	HandoffState   string
	TerminalStates []string
	PollingMS      int
	AgentKind      string
	// Model is the model set in the block named after the agent kind.
	Model string
	// Knobs holds the agent settings the file sets, keyed like the
	// Jigz config knobs (max_turns, polling_interval_ms, ...).
	Knobs map[string]string
	// Template is dispatch.default.template.
	Template string
	Rules    []Rule
}

type workflowFront struct {
	Tracker struct {
		Kind           string   `yaml:"kind"`
		QueryFilter    string   `yaml:"query_filter"`
		HandoffState   string   `yaml:"handoff_state"`
		TerminalStates []string `yaml:"terminal_states"`
	} `yaml:"tracker"`
	Polling struct {
		IntervalMS int `yaml:"interval_ms"`
	} `yaml:"polling"`
	Agent struct {
		Kind                string `yaml:"kind"`
		Command             string `yaml:"command"`
		MaxTurns            *int   `yaml:"max_turns"`
		MaxConcurrentAgents *int   `yaml:"max_concurrent_agents"`
		TurnTimeoutMS       *int   `yaml:"turn_timeout_ms"`
		ReadTimeoutMS       *int   `yaml:"read_timeout_ms"`
		StallTimeoutMS      *int   `yaml:"stall_timeout_ms"`
		StopGraceMS         *int   `yaml:"stop_grace_ms"`
		MaxRetryBackoffMS   *int   `yaml:"max_retry_backoff_ms"`
	} `yaml:"agent"`
	Dispatch struct {
		Default struct {
			Template string `yaml:"template"`
		} `yaml:"default"`
		Rules []struct {
			Name  string `yaml:"name"`
			Match struct {
				Labels []string `yaml:"labels"`
			} `yaml:"match"`
			Agent    string `yaml:"agent"`
			Template string `yaml:"template"`
			Stage    string `yaml:"stage"`
			Next     string `yaml:"next"`
		} `yaml:"rules"`
	} `yaml:"dispatch"`
	Blocks map[string]yaml.Node `yaml:",inline"`
}

// ParseWorkflow reads the front matter of a workflow file.
func ParseWorkflow(text string) (Workflow, error) {
	lines := strings.Split(text, "\n")
	start, end, err := frontMatter(lines)
	if err != nil {
		return Workflow{}, err
	}
	var f workflowFront
	if err := yaml.Unmarshal([]byte(strings.Join(lines[start+1:end], "\n")), &f); err != nil {
		return Workflow{}, err
	}
	w := Workflow{
		TrackerKind:    f.Tracker.Kind,
		QueryFilter:    f.Tracker.QueryFilter,
		HandoffState:   f.Tracker.HandoffState,
		TerminalStates: f.Tracker.TerminalStates,
		PollingMS:      f.Polling.IntervalMS,
		AgentKind:      f.Agent.Kind,
		Template:       f.Dispatch.Default.Template,
		Knobs:          map[string]string{},
	}
	for k, v := range map[string]string{"kind": f.Agent.Kind, "command": f.Agent.Command} {
		if v != "" {
			w.Knobs[k] = v
		}
	}
	for k, v := range map[string]*int{
		"max_turns":             f.Agent.MaxTurns,
		"max_concurrent_agents": f.Agent.MaxConcurrentAgents,
		"turn_timeout_ms":       f.Agent.TurnTimeoutMS,
		"read_timeout_ms":       f.Agent.ReadTimeoutMS,
		"stall_timeout_ms":      f.Agent.StallTimeoutMS,
		"stop_grace_ms":         f.Agent.StopGraceMS,
		"max_retry_backoff_ms":  f.Agent.MaxRetryBackoffMS,
		"polling_interval_ms":   pollingPtr(f.Polling.IntervalMS),
	} {
		if v != nil {
			w.Knobs[k] = strconv.Itoa(*v)
		}
	}
	if n, ok := f.Blocks[f.Agent.Kind]; ok {
		var b struct {
			Model string `yaml:"model"`
		}
		if n.Decode(&b) == nil {
			w.Model = b.Model
			if b.Model != "" {
				w.Knobs["model"] = b.Model
			}
		}
	}
	for _, r := range f.Dispatch.Rules {
		w.Rules = append(w.Rules, Rule{r.Name, r.Match.Labels, r.Agent, r.Template, r.Stage, r.Next})
	}
	return w, nil
}

func pollingPtr(ms int) *int {
	if ms == 0 {
		return nil
	}
	return &ms
}

// FilterLabels splits query_filter into the labels an issue may carry
// (label:a,b) and the labels that exclude it (-label:x).
func (w Workflow) FilterLabels() (include, exclude []string) {
	for _, tok := range strings.Fields(w.QueryFilter) {
		neg := strings.HasPrefix(tok, "-")
		list, ok := strings.CutPrefix(strings.TrimPrefix(tok, "-"), "label:")
		if !ok {
			continue
		}
		for _, l := range strings.Split(list, ",") {
			if l == "" {
				continue
			}
			if neg {
				exclude = append(exclude, l)
			} else {
				include = append(include, l)
			}
		}
	}
	return include, exclude
}

// StageLabel is the agent:* label that selects this workflow's issues,
// or the first included label when there is none.
func (w Workflow) StageLabel() string {
	include, _ := w.FilterLabels()
	for _, l := range include {
		if strings.HasPrefix(l, "agent:") {
			return l
		}
	}
	if len(include) > 0 {
		return include[0]
	}
	return ""
}

// RepoLabelOrder lists the label names of a repository's
// .sortie/config.yaml in file order, which is the order the stages are
// worked in.
func RepoLabelOrder(config string) []string {
	var c struct {
		Labels []struct {
			Name string `yaml:"name"`
		} `yaml:"labels"`
	}
	if yaml.Unmarshal([]byte(config), &c) != nil {
		return nil
	}
	var out []string
	for _, l := range c.Labels {
		out = append(out, l.Name)
	}
	return out
}
