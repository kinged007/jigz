package hubproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

var (
	repoRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	workflowRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	pathRe     = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	colorRe    = regexp.MustCompile(`^[0-9a-f]{6}$`)
	runnerRe   = regexp.MustCompile(`^runner:[a-z0-9][a-z0-9-]*$`)
)

var (
	outcomes = []OutcomeClass{OutcomeSucceeded, OutcomeFailedAgent, OutcomeFailedWorkspace, OutcomeFailedLimit, OutcomeCancelled, OutcomeTimeout, OutcomeHandoffWithheld}
	states   = []LoopState{LoopRunning, LoopSleeping, LoopStopped}
	details  = []LoopDetail{"", DetailPaused, DetailHeld, DetailDisabled, DetailExited, DetailStopping}
	commands = []CommandType{CmdStart, CmdStop, CmdDrain, CmdRestartWhenIdle, CmdPause, CmdResume, CmdWake, CmdReloadConfig, CmdSessionOpen, CmdSessionMessage, CmdSessionClose, CmdTaskRun}
	ackCodes = []AckCode{AckUnsupported, AckInvalid, AckFailed}
	errCodes = []ErrorCode{ErrInvalid, ErrUnsupportedVersion, ErrUnauthorized, ErrForbidden, ErrNotFound, ErrUnavailable, ErrInternal}
	events   = []EventType{EventStarted, EventFinished}
	kinds    = []ItemKind{KindIssue, KindPR}
)

// check records the first violated rule.
type check struct{ err error }

func (c *check) that(ok bool, field, rule string) {
	if !ok && c.err == nil {
		c.err = fmt.Errorf("%s: %s", field, rule)
	}
}

func (c *check) sub(field string, err error) {
	if err != nil && c.err == nil {
		c.err = fmt.Errorf("%s.%w", field, err)
	}
}

func (c *check) matches(re *regexp.Regexp, v, field string) {
	c.that(re.MatchString(v), field, "does not match "+re.String())
}

func (c *check) nonEmpty(v, field string) { c.that(v != "", field, "required") }

func (c *check) oneOf(ok bool, field string) { c.that(ok, field, "not an allowed value") }

func (r EnrollRequest) Validate() error {
	var c check
	c.nonEmpty(r.Token, "token")
	c.that(r.Name != "" && len(r.Name) <= 40, "name", "length 1 to 40")
	c.nonEmpty(r.Host.OS, "host.os")
	c.nonEmpty(r.Host.Arch, "host.arch")
	c.that(r.Host.CPUs >= 1, "host.cpus", "at least 1")
	c.that(r.Host.MemoryBytes >= 0, "host.memory_bytes", "negative")
	c.nonEmpty(r.JigVersion, "jig_version")
	c.nonEmpty(r.EngineVersion, "engine_version")
	c.nonEmpty(r.TrackerLogin, "tracker_login")
	return c.err
}

func (r EnrollResponse) Validate() error {
	var c check
	c.nonEmpty(r.DaemonID, "daemon_id")
	c.nonEmpty(r.Credential, "credential")
	c.matches(runnerRe, r.RunnerLabel, "runner_label")
	c.that(len(r.RunnerLabel) <= 50, "runner_label", "longer than 50")
	return c.err
}

func (r ConfigResponse) Validate() error {
	var c check
	c.nonEmpty(r.Member.Login, "member.login")
	c.that(r.Projects != nil, "projects", "required")
	c.that(r.Files != nil, "files", "required")
	c.that(r.Labels != nil, "labels", "required")
	for i, p := range r.Projects {
		f := fmt.Sprintf("projects[%d]", i)
		c.oneOf(p.Provider == ProviderGitHub, f+".provider")
		c.matches(repoRe, p.Repo, f+".repo")
		c.that(p.Workflows != nil, f+".workflows", "required")
		for _, w := range p.Workflows {
			c.matches(workflowRe, w, f+".workflows")
		}
		c.that(len(slices.Compact(slices.Sorted(slices.Values(p.Workflows)))) == len(p.Workflows), f+".workflows", "duplicates")
	}
	for i, file := range r.Files {
		f := fmt.Sprintf("files[%d]", i)
		c.matches(pathRe, file.Path, f+".path")
		c.matches(sha256Re, file.SHA256, f+".sha256")
	}
	for i, l := range r.Labels {
		f := fmt.Sprintf("labels[%d]", i)
		c.that(l.Name != "" && len(l.Name) <= 50, f+".name", "length 1 to 50")
		c.matches(colorRe, l.Color, f+".color")
	}
	return c.err
}

func (r StatusRequest) Validate() error {
	var c check
	c.that(!r.Time.IsZero(), "time", "required")
	c.that(r.Loops != nil, "loops", "required")
	c.that(r.Accounts != nil, "accounts", "required")
	c.that(r.Capacity != nil, "capacity", "required")
	if r.Capacity != nil {
		c.that(r.Capacity.MaxSessions >= 0, "capacity.max_sessions", "negative")
		c.that(r.Capacity.RunningSessions >= 0, "capacity.running_sessions", "negative")
	}
	c.that(r.Hours != nil, "hours", "required")
	for i, l := range r.Loops {
		f := fmt.Sprintf("loops[%d]", i)
		c.matches(repoRe, l.Project, f+".project")
		c.matches(workflowRe, l.Workflow, f+".workflow")
		c.oneOf(slices.Contains(states, l.State), f+".state")
		c.oneOf(slices.Contains(details, l.Detail), f+".detail")
		c.that(l.Running >= 0, f+".running", "negative")
		c.that(l.Retrying >= 0, f+".retrying", "negative")
		c.that(l.Files != nil, f+".files", "required")
		for j, h := range l.Files {
			c.matches(pathRe, h.Path, fmt.Sprintf("%s.files[%d].path", f, j))
			c.matches(sha256Re, h.SHA256, fmt.Sprintf("%s.files[%d].sha256", f, j))
		}
		if b := l.Budget; b != nil {
			c.nonEmpty(b.Period, f+".budget.period")
			c.that(b.MaxTokens >= 1, f+".budget.max_tokens", "at least 1")
			c.that(b.UsedTokens >= 0, f+".budget.used_tokens", "negative")
		}
	}
	for i, a := range r.Accounts {
		f := fmt.Sprintf("accounts[%d]", i)
		c.nonEmpty(a.Name, f+".name")
		c.nonEmpty(a.Runtime, f+".runtime")
		c.that((a.ExhaustedUntil == nil) == a.Available, f+".exhausted_until", "present exactly when not available")
	}
	return c.err
}

func (r StatusResponse) Validate() error {
	var c check
	c.that(r.IntervalSeconds >= 5, "interval_seconds", "at least 5")
	c.nonEmpty(r.ConfigETag, "config_etag")
	return c.err
}

func (r RunsRequest) Validate() error {
	var c check
	c.that(len(r.Events) >= 1 && len(r.Events) <= 100, "events", "1 to 100 items")
	for i, e := range r.Events {
		c.sub(fmt.Sprintf("events[%d]", i), e.Validate())
	}
	return c.err
}

func (e RunEvent) Validate() error {
	var c check
	c.that(e.ID != "" && len(e.ID) <= 200, "id", "length 1 to 200")
	c.oneOf(slices.Contains(events, e.Type), "type")
	c.matches(workflowRe, e.Workflow, "workflow")
	c.matches(repoRe, e.Item.Project, "item.project")
	c.that(e.Item.Number >= 1, "item.number", "at least 1")
	c.oneOf(slices.Contains(kinds, e.Item.Kind), "item.kind")
	c.that(e.Attempt >= 1, "attempt", "at least 1")
	c.that(!e.StartedAt.IsZero(), "started_at", "required")
	if e.Type == EventFinished {
		c.that(e.FinishedAt != nil, "finished_at", "required for finished")
		c.that(e.Usage != nil, "usage", "required for finished")
		c.that(e.Outcome != "", "outcome", "required for finished")
	} else {
		c.that(e.FinishedAt == nil && e.Usage == nil && e.Outcome == "", "type", "started carries finished_at, outcome or usage")
	}
	c.that(e.Outcome == "" || slices.Contains(outcomes, e.Outcome), "outcome", "not an allowed value")
	if u := e.Usage; u != nil {
		c.that(u.InputTokens >= 0 && u.OutputTokens >= 0 && u.TotalTokens >= 0, "usage", "negative tokens")
		c.that(u.CostUSD == nil || *u.CostUSD >= 0, "usage.cost_usd", "negative")
	}
	return c.err
}

func (r RunsResponse) Validate() error {
	var c check
	c.that(r.Accepted != nil, "accepted", "required")
	c.that(len(slices.Compact(slices.Sorted(slices.Values(r.Accepted)))) == len(r.Accepted), "accepted", "duplicates")
	c.that(!slices.Contains(r.Accepted, ""), "accepted", "empty id")
	return c.err
}

// LoopArgs decodes the arguments of a loop command and checks them.
func (cmd Command) LoopArgs() (LoopArgs, error) {
	var a LoopArgs
	dec := json.NewDecoder(bytes.NewReader(cmd.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, err
	}
	var c check
	if a.Project != "" {
		c.matches(repoRe, a.Project, "project")
	}
	if a.Workflow != "" {
		c.matches(workflowRe, a.Workflow, "workflow")
		c.that(a.Project != "", "workflow", "requires project")
	}
	if cmd.Type == CmdWake {
		c.that(a.Project != "" && a.Workflow != "", "args", "wake requires project and workflow")
	}
	return a, c.err
}

func (cmd Command) Validate() error {
	var c check
	c.that(cmd.ID != "" && len(cmd.ID) <= 200, "id", "length 1 to 200")
	c.oneOf(slices.Contains(commands, cmd.Type), "type")
	var obj map[string]json.RawMessage
	c.that(json.Unmarshal(cmd.Args, &obj) == nil && obj != nil, "args", "must be an object")
	if c.err != nil {
		return c.err
	}
	switch cmd.Type {
	case CmdStart, CmdStop, CmdDrain, CmdRestartWhenIdle, CmdWake:
		_, err := cmd.LoopArgs()
		c.sub("args", err)
	case CmdPause, CmdResume, CmdReloadConfig:
		c.that(len(obj) == 0, "args", "must be empty")
	}
	return c.err
}

func (a Ack) Validate() error {
	var c check
	c.that(a.ID != "" && len(a.ID) <= 200, "id", "length 1 to 200")
	c.that(a.Type == AckMessageType, "type", "must be ack")
	c.that(a.OK == (a.Error == nil), "error", "present exactly when ok is false")
	if a.Error != nil {
		c.oneOf(slices.Contains(ackCodes, a.Error.Code), "error.code")
	}
	return c.err
}

func (e ErrorBody) Validate() error {
	var c check
	c.oneOf(slices.Contains(errCodes, e.Code), "code")
	c.that((e.Supported != nil) == (e.Code == ErrUnsupportedVersion), "supported", "present exactly for unsupported_version")
	if e.Supported != nil {
		c.that(e.Supported.Min >= 0 && e.Supported.Max >= 0, "supported", "negative")
	}
	return c.err
}
