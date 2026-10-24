// Package hubstate stores the enrollment of this machine with one hub.
package hubstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kinged007/jigz/internal/dirs"
)

// State is the content of hub.json. Credential and TrackerToken are secrets.
type State struct {
	HubURL       string `json:"hub_url"`
	DaemonID     string `json:"daemon_id"`
	Name         string `json:"name"`
	Credential   string `json:"credential"`
	RunnerLabel  string `json:"runner_label"`
	TrackerToken string `json:"tracker_token"`
}

// Path returns <config>/jigz/hub.json.
func Path() (string, error) {
	dir, err := dirs.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hub.json"), nil
}

// Load reads hub.json. It reports fs.ErrNotExist when the machine is not
// enrolled. It refuses a symlink, a file another user owns, a file that
// group or others can access and a directory that group or others can write.
func Load() (State, error) {
	var s State
	path, err := Path()
	if err != nil {
		return s, err
	}
	dir := filepath.Dir(path)
	dinfo, err := os.Stat(dir)
	if err != nil {
		return s, err
	}
	if dinfo.Mode().Perm()&0o022 != 0 {
		return s, fmt.Errorf("%s is writable by group or others (mode %04o): run chmod 700 on it", dir, dinfo.Mode().Perm())
	}
	linfo, err := os.Lstat(path)
	if err != nil {
		return s, err
	}
	if !linfo.Mode().IsRegular() {
		return s, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	// The checks apply to the opened file, so a swap after Lstat is seen.
	info, err := f.Stat()
	if err != nil {
		return s, err
	}
	if !os.SameFile(linfo, info) {
		return s, fmt.Errorf("%s changed while it was opened", path)
	}
	if err := checkOwner(path, info); err != nil {
		return s, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return s, fmt.Errorf("%s is accessible by group or others (mode %04o): run chmod 600 on it", path, info.Mode().Perm())
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("%s: invalid JSON", path)
	}
	return s, nil
}

// Exists reports whether hub.json is present.
func Exists() bool {
	path, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Save writes hub.json through a 0600 temporary file that is renamed into
// place, in a 0700 directory.
func Save(s State) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hub.json.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Remove deletes hub.json. A missing file is not an error.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
