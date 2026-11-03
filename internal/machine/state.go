package machine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/dirs"
)

// EntryState is the recorded state of one agent: a pool entry or a fleet
// agent of a work entry.
type EntryState struct {
	ExhaustedUntil time.Time `json:"exhausted_until,omitzero"`
	Reason         string    `json:"reason,omitempty"`
	LastUsed       time.Time `json:"last_used,omitzero"`
	// Model is the configured model that failed with an unknown-model
	// error; the block holds until the config names a different one.
	Model string `json:"model,omitempty"`
}

// LoopState is the recorded state of one loop: the agent the manager
// selected, why the loop is offline and when it is expected to start.
// The current agent is recomputed after a restart.
type LoopState struct {
	Agent         string    `json:"agent,omitempty"`
	OfflineReason string    `json:"offline_reason,omitempty"`
	ETA           time.Time `json:"eta,omitzero"`
}

// State is the runtime state of one repository: per-agent records keyed
// by agent name and per-loop records keyed by loop name. Names missing
// from the config are ignored by the selection functions.
type State struct {
	Agents map[string]EntryState `json:"agents,omitempty"`
	Loops  map[string]LoopState  `json:"loops,omitempty"`
}

const maxReason = 200

// StatePath returns <root>/.jigz/data/state.json.
func StatePath(root string) string {
	return filepath.Join(root, ".jigz", "data", "state.json")
}

// legacyStatePath returns <state dir>/jigz/pool.json, the per-user file
// the state lived in before it moved into the repository. It is read
// once when the repository state file does not exist yet and never
// written again.
func legacyStatePath() (string, error) {
	dir, err := dirs.State()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pool.json"), nil
}

// LoadState reads the state file. A missing file falls back to the old
// per-user pool.json so limits recorded before the move still count; a
// missing both gives an empty State.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return loadLegacyState()
	}
	if err != nil {
		return State{}, err
	}
	st := State{}
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, err
	}
	return st, nil
}

// loadLegacyState reads the old flat pool.json shape: agent names mapped
// to their state, no loop records.
func loadLegacyState() (State, error) {
	legacy, err := legacyStatePath()
	if err != nil {
		return State{}, nil
	}
	raw, err := os.ReadFile(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	agents := map[string]EntryState{}
	if err := json.Unmarshal(raw, &agents); err != nil {
		return State{}, err
	}
	return State{Agents: agents}, nil
}

// UpdateState runs fn on the current state and saves the result, holding an
// exclusive lock so concurrent processes do not lose updates.
func UpdateState(path string, fn func(*State)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	st, err := LoadState(path)
	if err != nil {
		return err
	}
	if st.Agents == nil {
		st.Agents = map[string]EntryState{}
	}
	if st.Loops == nil {
		st.Loops = map[string]LoopState{}
	}
	fn(&st)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MarkUsed records the time an agent was last picked.
func MarkUsed(path, name string, at time.Time) error {
	return UpdateState(path, func(st *State) {
		e := st.Agents[name]
		e.LastUsed = at
		st.Agents[name] = e
	})
}

// MarkExhausted records that an agent hit a usage limit until the given time.
func MarkExhausted(path, name string, until time.Time, reason string) error {
	return UpdateState(path, func(st *State) {
		e := st.Agents[name]
		e.ExhaustedUntil, e.Reason, e.Model = until, trimReason(reason), ""
		st.Agents[name] = e
	})
}

// MarkModelExhausted records that an agent failed with an unknown-model
// error and remembers the configured model that failed, so the block
// holds until the config names a different model.
func MarkModelExhausted(path, name string, until time.Time, model, reason string) error {
	return UpdateState(path, func(st *State) {
		e := st.Agents[name]
		e.ExhaustedUntil, e.Reason, e.Model = until, trimReason(reason), model
		st.Agents[name] = e
	})
}

// ModelStale reports whether a model-unavailable block no longer holds
// because the configured model changed since the failure. Usage-limit
// blocks never go stale.
func (e EntryState) ModelStale(model string) bool {
	return strings.HasPrefix(e.Reason, "model unavailable:") && e.Model != model
}

// trimReason caps a record reason at maxReason runes.
func trimReason(reason string) string {
	if r := []rune(reason); len(r) > maxReason {
		return string(r[:maxReason])
	}
	return reason
}
