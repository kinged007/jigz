package machine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// EntryState is the recorded state of one pool entry.
type EntryState struct {
	ExhaustedUntil time.Time `json:"exhausted_until,omitzero"`
	Reason         string    `json:"reason,omitempty"`
	LastUsed       time.Time `json:"last_used,omitzero"`
}

// State maps pool entry names to their state. Names missing from the pool
// config are ignored by the selection functions.
type State map[string]EntryState

const maxReason = 200

// StatePath returns <state dir>/sortie-loop/pool.json, with the state dir
// taken from XDG_STATE_HOME or ~/.local/state.
func StatePath() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "sortie-loop", "pool.json"), nil
}

// LoadState reads the state file. A missing file gives an empty State.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return nil, err
	}
	st := State{}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return st, nil
}

// UpdateState runs fn on the current state and saves the result, holding an
// exclusive lock so concurrent processes do not lose updates.
func UpdateState(path string, fn func(State)) error {
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
	fn(st)
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

// MarkUsed records the time an entry was last picked.
func MarkUsed(path, name string, at time.Time) error {
	return UpdateState(path, func(st State) {
		e := st[name]
		e.LastUsed = at
		st[name] = e
	})
}

// MarkExhausted records that an entry hit a usage limit until the given time.
func MarkExhausted(path, name string, until time.Time, reason string) error {
	if r := []rune(reason); len(r) > maxReason {
		reason = string(r[:maxReason])
	}
	return UpdateState(path, func(st State) {
		e := st[name]
		e.ExhaustedUntil, e.Reason = until, reason
		st[name] = e
	})
}
