package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kinged007/jigz/internal/machine"
)

// StringOrRef is a plain string or a reference to an environment variable
// of the supervising process, {from_env: NAME}. Credentials must be
// references; the validator enforces that by key name (isSecretName).
type StringOrRef struct {
	Str     string
	FromEnv string
}

// set reports whether the value was written in the document.
func (v StringOrRef) set() bool { return v.Str != "" || v.FromEnv != "" }

// ref reports whether the value is a {from_env: NAME} reference.
func (v StringOrRef) ref() bool { return v.FromEnv != "" }

// resolve returns the referenced value from the process environment. An
// empty variable counts as unresolved.
func (v StringOrRef) resolve() (string, bool) {
	if v.FromEnv == "" {
		return v.Str, true
	}
	s := os.Getenv(v.FromEnv)
	return s, s != ""
}

func (v *StringOrRef) UnmarshalYAML(node *yaml.Node) error {
	switch {
	case node.Kind == yaml.ScalarNode:
		v.Str = node.Value
	case node.Kind == yaml.MappingNode:
		var r struct {
			FromEnv string `yaml:"from_env"`
		}
		if err := node.Decode(&r); err != nil {
			return err
		}
		v.FromEnv = r.FromEnv
	default:
		return errors.New("expected a string or a {from_env: NAME} reference")
	}
	return nil
}

func (v *StringOrRef) checkNodeKeys(node *yaml.Node, path string) error {
	return checkMapKeys(node, path, "from_env")
}

// NameList is one name or a list of names, in fallback order.
type NameList []string

func (n *NameList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Tag == "!!null" {
			return nil
		}
		*n = []string{node.Value}
		return nil
	}
	var names []string
	if err := node.Decode(&names); err != nil {
		return err
	}
	*n = names
	return nil
}

// AgentRef is a rule's agent selection: one name, a list of names, or a
// mapping {use, model, schedule} that overrides the fleet agent for the
// rule. An empty selection inherits the work entry's agent.
type AgentRef struct {
	Names    []string
	Model    string
	Schedule *machine.Hours

	mapping bool
}

func (a *AgentRef) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return nil
		}
		a.Names = []string{node.Value}
	case yaml.SequenceNode:
		var names []string
		if err := node.Decode(&names); err != nil {
			return err
		}
		a.Names = names
	case yaml.MappingNode:
		var m struct {
			Use      string         `yaml:"use"`
			Model    string         `yaml:"model"`
			Schedule *machine.Hours `yaml:"schedule"`
		}
		if err := node.Decode(&m); err != nil {
			return err
		}
		a.mapping = true
		if m.Use != "" {
			a.Names = []string{m.Use}
		}
		a.Model = m.Model
		a.Schedule = m.Schedule
	default:
		return errors.New("expected a name, a list of names, or a {use: ...} mapping")
	}
	return nil
}

func (a *AgentRef) checkNodeKeys(node *yaml.Node, path string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		switch key {
		case "use", "model":
		case "schedule":
			if err := checkKeys(node.Content[i+1], machineHoursType, joinKeyPath(path, key)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown key %q", joinKeyPath(path, key))
		}
	}
	return nil
}

var machineHoursType = reflect.TypeOf(machine.Hours{})

// Tracker is the tracker block: the defaults every work entry may
// override. api_key must be a reference; project may also be a plain
// string because it names a project, not a credential.
type Tracker struct {
	Kind            string      `yaml:"kind"`
	ApiKey          StringOrRef `yaml:"api_key"`
	Project         StringOrRef `yaml:"project"`
	ActiveStates    []string    `yaml:"active_states"`
	InProgressState string      `yaml:"in_progress_state"`
	HandoffState    string      `yaml:"handoff_state"`
}

// Hooks is the hooks block: commands run around a session. timeout_ms of
// 0 leaves the engine default.
type Hooks struct {
	AfterCreate string `yaml:"after_create"`
	BeforeRun   string `yaml:"before_run"`
	AfterRun    string `yaml:"after_run"`
	TimeoutMS   int    `yaml:"timeout_ms"`
}

// Agent is one entry of the fleet: a runtime with a model, an optional
// schedule (omitted = always available) and the agent process's
// environment. env values are strings or {from_env} references; a key
// that names a credential must be a reference.
type Agent struct {
	Runtime  string                 `yaml:"runtime"`
	Model    string                 `yaml:"model"`
	Schedule machine.Hours          `yaml:"schedule"`
	Env      map[string]StringOrRef `yaml:"env"`
}

// Match is the engine's rule match block.
type Match struct {
	Labels []string `yaml:"labels"`
}

// Rule is one dispatch rule of a work entry. Stage and Next are the
// engine's stage chaining, carried through uninterpreted.
type Rule struct {
	Name   string   `yaml:"name"`
	Match  Match    `yaml:"match"`
	Prompt string   `yaml:"prompt"`
	Agent  AgentRef `yaml:"agent"`
	Next   string   `yaml:"next"`
	Stage  string   `yaml:"stage"`
}

// Work is one entry of the work: section after defaults that only the
// loader can apply (the budget) are resolved. Tracker and Hooks are the
// entry's overrides of the file-level defaults; nil means none. Agent
// names may name a pool.
type Work struct {
	Managed             bool
	QueryFilter         string
	Tracker             *Tracker
	Hooks               *Hooks
	MaxConcurrentAgents int
	MaxTurns            int
	PollingIntervalMS   int
	Budget              *Budget
	Agent               []string
	Rules               []Rule
}

// workFile is the YAML shape of a work entry.
type workFile struct {
	Managed             *bool       `yaml:"managed"`
	QueryFilter         string      `yaml:"query_filter"`
	Tracker             *Tracker    `yaml:"tracker"`
	Hooks               *Hooks      `yaml:"hooks"`
	MaxConcurrentAgents int         `yaml:"max_concurrent_agents"`
	MaxTurns            int         `yaml:"max_turns"`
	PollingIntervalMS   int         `yaml:"polling_interval_ms"`
	Budget              *budgetFile `yaml:"budget"`
	Agent               NameList    `yaml:"agent"`
	Rules               []Rule      `yaml:"rules"`
}

func (f workFile) resolve(slug string) (Work, error) {
	w := Work{
		Managed: true, QueryFilter: f.QueryFilter, Tracker: f.Tracker, Hooks: f.Hooks,
		MaxConcurrentAgents: f.MaxConcurrentAgents, MaxTurns: f.MaxTurns,
		PollingIntervalMS: f.PollingIntervalMS, Agent: f.Agent, Rules: f.Rules,
	}
	if f.Managed != nil {
		w.Managed = *f.Managed
	}
	if f.Budget != nil {
		b, err := f.Budget.resolve("work." + slug + ".budget.")
		if err != nil {
			return Work{}, err
		}
		w.Budget = &b
	}
	return w, nil
}

// ResolvedRule is one rule of a resolved work entry with its agent
// selection applied. Agents are the ordered candidate agent names (pools
// expanded); when the rule has no selection they are the entry's.
// Model and Schedule override the fleet agent for this rule.
type ResolvedRule struct {
	Name     string
	Labels   []string
	Prompt   string
	Stage    string
	Next     string
	Agents   []string
	Model    string
	Schedule *machine.Hours
}

// Resolved is one work entry with the file-level defaults and the
// entry's overrides applied. Engine is the engine target; empty fields
// mean the engine defaults (kinged007/sortie v1.28.1). OperatingHours
// with no windows means 24/7. A nil Budget means the entry has none.
// This is plain data: resolving does no file I/O.
type Resolved struct {
	Slug                string
	Managed             bool
	QueryFilter         string
	Tracker             Tracker
	Hooks               Hooks
	MaxConcurrentAgents int
	MaxTurns            int
	PollingIntervalMS   int
	Budget              *Budget
	Agents              []string
	Rules               []ResolvedRule
	Engine              machine.Engine
	OperatingHours      machine.Hours
}

// Resolve turns the loaded config into one Resolved per work entry.
func (c *Config) Resolve() map[string]Resolved {
	out := map[string]Resolved{}
	for _, slug := range sortedNames(c.Work) {
		w := c.Work[slug]
		r := Resolved{
			Slug: slug, Managed: w.Managed, QueryFilter: w.QueryFilter,
			Tracker: c.TrackerDefaults, Hooks: c.Hooks,
			MaxConcurrentAgents: w.MaxConcurrentAgents, MaxTurns: w.MaxTurns,
			PollingIntervalMS: w.PollingIntervalMS, Budget: w.Budget,
			Engine: c.Engine, OperatingHours: c.OperatingHours,
		}
		r.OperatingHours.End = endMode(r.OperatingHours.End)
		if w.Tracker != nil {
			r.Tracker = mergeTracker(r.Tracker, *w.Tracker)
		}
		if w.Hooks != nil {
			r.Hooks = mergeHooks(r.Hooks, *w.Hooks)
		}
		r.Agents = c.expand(w.Agent)
		for _, rule := range w.Rules {
			rr := ResolvedRule{
				Name: rule.Name, Labels: rule.Match.Labels, Prompt: rule.Prompt,
				Stage: rule.Stage, Next: rule.Next, Model: rule.Agent.Model,
				Schedule: rule.Agent.Schedule,
			}
			if rr.Schedule != nil {
				s := *rr.Schedule
				s.End = endMode(s.End)
				rr.Schedule = &s
			}
			names := rule.Agent.Names
			if len(names) == 0 {
				names = w.Agent
			}
			rr.Agents = c.expand(names)
			r.Rules = append(r.Rules, rr)
		}
		out[slug] = r
	}
	return out
}

// expand maps names to ordered agent names: a pool contributes its
// entries in pool order, anything else is an agent name.
func (c *Config) expand(names []string) []string {
	var out []string
	for _, n := range names {
		if agents, ok := c.Pools[n]; ok {
			out = append(out, agents...)
		} else {
			out = append(out, n)
		}
	}
	return out
}

// mergeTracker overlays the set fields of over onto base.
func mergeTracker(base, over Tracker) Tracker {
	if over.Kind != "" {
		base.Kind = over.Kind
	}
	if over.ApiKey.set() {
		base.ApiKey = over.ApiKey
	}
	if over.Project.set() {
		base.Project = over.Project
	}
	if over.ActiveStates != nil {
		base.ActiveStates = over.ActiveStates
	}
	if over.InProgressState != "" {
		base.InProgressState = over.InProgressState
	}
	if over.HandoffState != "" {
		base.HandoffState = over.HandoffState
	}
	return base
}

// mergeHooks overlays the set fields of over onto base.
func mergeHooks(base, over Hooks) Hooks {
	if over.AfterCreate != "" {
		base.AfterCreate = over.AfterCreate
	}
	if over.BeforeRun != "" {
		base.BeforeRun = over.BeforeRun
	}
	if over.AfterRun != "" {
		base.AfterRun = over.AfterRun
	}
	if over.TimeoutMS != 0 {
		base.TimeoutMS = over.TimeoutMS
	}
	return base
}

// endMode maps an unset end mode to the default.
func endMode(end string) string {
	if end == "" {
		return machine.EndFinish
	}
	return end
}

// RefStatus reports the from_env references that do not resolve against
// the process environment. An empty variable counts as unresolved.
type RefStatus struct {
	// Tracker holds the unresolved references of the tracker defaults
	// and the per-entry overrides.
	Tracker []string
	// Agents maps an agent name to its unresolved env references. An
	// agent with entries is unavailable.
	Agents map[string][]string
}

// UnresolvedRefs reports the from_env references without a value in the
// process environment. It is the reason an agent is unavailable; the
// load does not fail on it.
func (c *Config) UnresolvedRefs() RefStatus {
	st := RefStatus{Agents: map[string][]string{}}
	missing := func(v StringOrRef) (string, bool) {
		if !v.ref() {
			return "", false
		}
		if _, ok := v.resolve(); !ok {
			return v.FromEnv, true
		}
		return "", false
	}
	seen := map[string]bool{}
	addTracker := func(t Tracker) {
		for _, v := range []StringOrRef{t.ApiKey, t.Project} {
			if name, ok := missing(v); ok && !seen[name] {
				seen[name] = true
				st.Tracker = append(st.Tracker, name)
			}
		}
	}
	addTracker(c.TrackerDefaults)
	for _, w := range c.Work {
		if w.Tracker != nil {
			addTracker(*w.Tracker)
		}
	}
	sort.Strings(st.Tracker)
	for _, name := range sortedNames(c.Agents) {
		for _, key := range sortedNames(c.Agents[name].Env) {
			if ref, ok := missing(c.Agents[name].Env[key]); ok {
				st.Agents[name] = append(st.Agents[name], ref)
			}
		}
	}
	return st
}

var secretSuffixes = []string{"TOKEN", "KEY", "SECRET", "PASSWORD"}

// isSecretName reports whether a variable name must hold a {from_env}
// reference and never a literal. The suffixes match in any case.
func isSecretName(name string) bool {
	u := strings.ToUpper(name)
	for _, s := range secretSuffixes {
		if strings.HasSuffix(u, s) {
			return true
		}
	}
	return false
}

var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// validateModel validates the values of the new sections and every
// cross-reference between them. Error messages name the field path.
func (c *Config) validateModel() error {
	if c.Engine.Repo != "" {
		if err := machine.ValidateEngineRepo(c.Engine.Repo); err != nil {
			return fmt.Errorf("engine.repo: %w", err)
		}
	}
	if c.Engine.Version != "" {
		if err := machine.ValidateEngineVersion(c.Engine.Version); err != nil {
			return fmt.Errorf("engine.version: %w", err)
		}
	}
	if err := c.OperatingHours.Validate("operating_hours"); err != nil {
		return err
	}
	if err := validateEnd(c.OperatingHours.End, "operating_hours"); err != nil {
		return err
	}
	if err := validateTracker(c.TrackerDefaults, "tracker"); err != nil {
		return err
	}
	if err := validateHooks(c.Hooks, "hooks"); err != nil {
		return err
	}
	for _, name := range sortedNames(c.Pools) {
		if _, ok := c.Agents[name]; ok {
			return fmt.Errorf("pools.%s: name is also an agent; agents and pools share one namespace", name)
		}
		for _, n := range c.Pools[name] {
			if _, ok := c.Agents[n]; !ok {
				return fmt.Errorf("pools.%s: unknown agent %q", name, n)
			}
		}
	}
	for _, name := range sortedNames(c.Agents) {
		a := c.Agents[name]
		key := "agents." + name
		if !slices.Contains(machine.Runtimes, a.Runtime) {
			return fmt.Errorf("%s.runtime: unknown runtime %q (use %s)", key, a.Runtime, strings.Join(machine.Runtimes, " or "))
		}
		if err := a.Schedule.Validate(key + ".schedule"); err != nil {
			return err
		}
		if err := validateEnd(a.Schedule.End, key+".schedule"); err != nil {
			return err
		}
		for _, envKey := range sortedNames(a.Env) {
			if isSecretName(envKey) && !a.Env[envKey].ref() {
				return fmt.Errorf("%s.env.%s: a credential must be a {from_env: NAME} reference, never a literal", key, envKey)
			}
		}
	}
	for _, slug := range sortedNames(c.Work) {
		if err := c.validateWorkEntry(slug); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateWorkEntry(slug string) error {
	key := "work." + slug
	w := c.Work[slug]
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("%s: work names are letters, digits and \"-\"", key)
	}
	if w.Tracker != nil {
		if err := validateTracker(*w.Tracker, key+".tracker"); err != nil {
			return err
		}
	}
	if w.Hooks != nil {
		if err := validateHooks(*w.Hooks, key+".hooks"); err != nil {
			return err
		}
	}
	for _, n := range w.Agent {
		if !c.knownName(n) {
			return fmt.Errorf("%s.agent: unknown agent or pool %q", key, n)
		}
	}
	byName := map[string]int{}
	for i, r := range w.Rules {
		rkey := fmt.Sprintf("%s.rules[%d]", key, i)
		if r.Name == "" {
			return fmt.Errorf("%s.name is required", rkey)
		}
		if _, ok := byName[r.Name]; ok {
			return fmt.Errorf("%s.name: %q is used twice", rkey, r.Name)
		}
		byName[r.Name] = i
		if r.Agent.mapping && len(r.Agent.Names) == 0 {
			return fmt.Errorf("%s.agent: use is required in the mapping form", rkey)
		}
		for _, n := range r.Agent.Names {
			if !c.knownName(n) {
				return fmt.Errorf("%s.agent: unknown agent or pool %q", rkey, n)
			}
		}
		if r.Agent.Schedule != nil {
			if err := r.Agent.Schedule.Validate(rkey + ".agent.schedule"); err != nil {
				return err
			}
			if err := validateEnd(r.Agent.Schedule.End, rkey+".agent.schedule"); err != nil {
				return err
			}
		}
	}
	for i, r := range w.Rules {
		if r.Next == "" {
			continue
		}
		if j, ok := byName[r.Next]; !ok || j <= i {
			return fmt.Errorf("%s.rules[%d].next: %q does not name a later rule of %s", key, i, r.Next, key)
		}
	}
	return nil
}

func (c *Config) knownName(name string) bool {
	if _, ok := c.Agents[name]; ok {
		return true
	}
	_, ok := c.Pools[name]
	return ok
}

func validateTracker(t Tracker, key string) error {
	if t.ApiKey.Str != "" {
		return fmt.Errorf("%s.api_key: a credential must be a {from_env: NAME} reference, never a literal", key)
	}
	return nil
}

func validateHooks(h Hooks, key string) error {
	if h.TimeoutMS < 0 {
		return fmt.Errorf("%s.timeout_ms must not be negative", key)
	}
	return nil
}

func validateEnd(end, key string) error {
	switch end {
	case "", machine.EndFinish, machine.EndStop:
		return nil
	}
	return fmt.Errorf("%s.end: %q is not finish or stop", key, end)
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
