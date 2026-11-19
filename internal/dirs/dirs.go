// Package dirs resolves the per-user directories Jigz keeps its files in.
// Each one is named after the product. A directory left behind by an
// install from before the rename is moved into place the first time it
// is looked up, so an upgrade keeps the settings, history and registry
// the user already has.
package dirs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// Name is the directory name under the user config, cache, state and
	// data roots that holds Jigz's files.
	Name = "jigz"
	// LegacyName is the name used before the rename.
	LegacyName = "sortie-loop"
	// legacyDashboardName is the earlier name of the dashboard's state
	// directory, which shared the state root with the pool state.
	legacyDashboardName = "sortie-central"
)

// Config returns the user config directory: <user config>/jigz.
func Config() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return Root(root)
}

// Cache returns the user cache directory: <user cache>/jigz.
func Cache() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return Root(root)
}

// State returns the user state directory: <user state>/jigz.
func State() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	return stateRoot(root)
}

// Data returns the user data directory: <user data>/jigz. A failure to
// move an old directory is not reported: the directory holds a download
// that can be fetched again.
func Data() string {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", Name)
		}
		root = filepath.Join(home, ".local", "share")
	}
	dir, err := Root(root)
	if err != nil {
		return filepath.Join(root, Name)
	}
	return dir
}

// Root returns the Jigz directory under the given user root. A directory
// left there by an install from before the rename is moved into place.
// The move is reported when it fails, because the old directory holds
// the user's settings and starting with an empty one would lose them.
func Root(userRoot string) (string, error) {
	return move(filepath.Join(userRoot, Name), filepath.Join(userRoot, LegacyName))
}

// stateRoot returns the state directory. The pool state and the
// dashboard state were two directories before the rename and are one
// now, so a second directory is merged into the first.
func stateRoot(root string) (string, error) {
	dst := filepath.Join(root, Name)
	for _, legacy := range []string{LegacyName, legacyDashboardName} {
		src := filepath.Join(root, legacy)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Stat(dst); err != nil {
			if _, err := move(dst, src); err != nil {
				return dst, err
			}
			continue
		}
		if err := merge(src, dst); err != nil {
			return dst, err
		}
	}
	return dst, nil
}

// move renames src to dst when dst is not there yet.
func move(dst, src string) (string, error) {
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if _, err := os.Stat(src); err != nil {
		return dst, nil
	}
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Another process moved it first.
			return dst, nil
		}
		return dst, fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	return dst, nil
}

// merge moves the entries of src into dst. A name that is already in
// dst is left alone.
func merge(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("move %s to %s: %w", from, to, err)
		}
	}
	return nil
}
