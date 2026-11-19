// Package hubproto holds the Go types of the hub protocol v0 messages. The
// JSON Schemas in schemas/hub-protocol/v0 describe the same messages; the
// contract test in this package keeps both in step.
package hubproto

import (
	"encoding/json"
	"time"
)

type OutcomeClass string

const (
	OutcomeSucceeded       OutcomeClass = "succeeded"
	OutcomeFailedAgent     OutcomeClass = "failed_agent"
	OutcomeFailedWorkspace OutcomeClass = "failed_workspace"
	OutcomeFailedLimit     OutcomeClass = "failed_limit"
	OutcomeCancelled       OutcomeClass = "cancelled"
	OutcomeTimeout         OutcomeClass = "timeout"
	OutcomeHandoffWithheld OutcomeClass = "handoff_withheld"
)

type LoopState string

const (
	LoopRunning  LoopState = "running"
	LoopSleeping LoopState = "sleeping"
	LoopStopped  LoopState = "stopped"
)

type LoopDetail string

const (
	DetailPaused   LoopDetail = "paused"
	DetailHeld     LoopDetail = "held"
	DetailDisabled LoopDetail = "disabled"
	DetailExited   LoopDetail = "exited"
	DetailStopping LoopDetail = "stopping"
)

type CommandType string

const (
	CmdStart           CommandType = "start"
	CmdStop            CommandType = "stop"
	CmdDrain           CommandType = "drain"
	CmdRestartWhenIdle CommandType = "restart_when_idle"
	CmdPause           CommandType = "pause"
	CmdResume          CommandType = "resume"
	CmdWake            CommandType = "wake"
	CmdReloadConfig    CommandType = "reload_config"
	CmdSessionOpen     CommandType = "session.open"
	CmdSessionMessage  CommandType = "session.message"
	CmdSessionClose    CommandType = "session.close"
	CmdTaskRun         CommandType = "task.run"
)

type AckCode string

const (
	AckUnsupported AckCode = "unsupported"
	AckInvalid     AckCode = "invalid"
	AckFailed      AckCode = "failed"
)

type ErrorCode string

const (
	ErrInvalid            ErrorCode = "invalid"
	ErrUnsupportedVersion ErrorCode = "unsupported_version"
	ErrUnauthorized       ErrorCode = "unauthorized"
	ErrForbidden          ErrorCode = "forbidden"
	ErrNotFound           ErrorCode = "not_found"
	ErrUnavailable        ErrorCode = "unavailable"
	ErrInternal           ErrorCode = "internal"
)

type EventType string

const (
	EventStarted  EventType = "started"
	EventFinished EventType = "finished"
)

type ItemKind string

const (
	KindIssue ItemKind = "issue"
	KindPR    ItemKind = "pr"
)

const (
	ProviderGitHub = "github"
	AckMessageType = "ack"
)

type EnrollRequest struct {
	Token         string `json:"token"`
	Name          string `json:"name"`
	Host          Host   `json:"host"`
	JigVersion    string `json:"jig_version"`
	EngineVersion string `json:"engine_version"`
	TrackerLogin  string `json:"tracker_login"`
}

type Host struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUs        int    `json:"cpus"`
	MemoryBytes int64  `json:"memory_bytes"`
}

type EnrollResponse struct {
	DaemonID    string `json:"daemon_id"`
	Credential  string `json:"credential"`
	RunnerLabel string `json:"runner_label"`
}

type ConfigResponse struct {
	Member   Member          `json:"member"`
	Projects []ConfigProject `json:"projects"`
	Files    []ConfigFile    `json:"files"`
	Labels   []Label         `json:"labels"`
}

type Member struct {
	Login string `json:"login"`
}

type ConfigProject struct {
	Provider        string   `json:"provider"`
	Repo            string   `json:"repo"`
	Workflows       []string `json:"workflows"`
	FilterAdditions string   `json:"filter_additions"`
}

type ConfigFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type StatusRequest struct {
	Time      time.Time `json:"time"`
	Paused    bool      `json:"paused"`
	Loops     []Loop    `json:"loops"`
	Capacity  *Capacity `json:"capacity"`
	Hours     *Hours    `json:"hours"`
	Accounts  []Account `json:"accounts"`
	LastError string    `json:"last_error,omitempty"`
}

type Loop struct {
	Project  string     `json:"project"`
	Workflow string     `json:"workflow"`
	State    LoopState  `json:"state"`
	Detail   LoopDetail `json:"detail,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Running  int        `json:"running"`
	Retrying int        `json:"retrying"`
	Files    []FileHash `json:"files"`
	Budget   *Budget    `json:"budget,omitempty"`
}

type FileHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Budget struct {
	Period     string `json:"period"`
	MaxTokens  int64  `json:"max_tokens"`
	UsedTokens int64  `json:"used_tokens"`
}

type Capacity struct {
	MaxSessions     int `json:"max_sessions"`
	RunningSessions int `json:"running_sessions"`
}

type Hours struct {
	Open       bool       `json:"open"`
	NextChange *time.Time `json:"next_change,omitempty"`
}

type Account struct {
	Name           string     `json:"name"`
	Runtime        string     `json:"runtime"`
	Available      bool       `json:"available"`
	ExhaustedUntil *time.Time `json:"exhausted_until,omitempty"`
}

type StatusResponse struct {
	IntervalSeconds int    `json:"interval_seconds"`
	ConfigETag      string `json:"config_etag"`
}

type RunsRequest struct {
	Events []RunEvent `json:"events"`
}

type RunEvent struct {
	ID         string       `json:"id"`
	Type       EventType    `json:"type"`
	Workflow   string       `json:"workflow"`
	Item       Item         `json:"item"`
	Attempt    int          `json:"attempt"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Outcome    OutcomeClass `json:"outcome,omitempty"`
	Usage      *Usage       `json:"usage,omitempty"`
	Links      *Links       `json:"links,omitempty"`
}

type Item struct {
	Project string   `json:"project"`
	Number  int      `json:"number"`
	Kind    ItemKind `json:"kind"`
}

type Usage struct {
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	Measured     bool     `json:"measured"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

type Links struct {
	Branch  string `json:"branch,omitempty"`
	PR      string `json:"pr,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type RunsResponse struct {
	Accepted []string `json:"accepted"`
}

// Command is a hub to daemon message. Args holds the JSON object; LoopArgs
// decodes it for the loop commands.
type Command struct {
	ID   string          `json:"id"`
	Type CommandType     `json:"type"`
	Args json.RawMessage `json:"args"`
}

// LoopArgs are the arguments of the loop commands (start, stop, drain,
// restart_when_idle, wake). Both fields are empty for all loops.
type LoopArgs struct {
	Project  string `json:"project,omitempty"`
	Workflow string `json:"workflow,omitempty"`
}

type Ack struct {
	ID    string    `json:"id"`
	Type  string    `json:"type"`
	OK    bool      `json:"ok"`
	Error *AckError `json:"error,omitempty"`
}

type AckError struct {
	Code    AckCode `json:"code"`
	Message string  `json:"message"`
}

type ErrorBody struct {
	Code      ErrorCode  `json:"code"`
	Message   string     `json:"message"`
	Supported *Supported `json:"supported,omitempty"`
}

type Supported struct {
	Min int `json:"min"`
	Max int `json:"max"`
}
