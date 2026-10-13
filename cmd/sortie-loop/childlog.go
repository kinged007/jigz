package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	childLogMaxBytes = 10 << 20
	childLogKeep     = 3
)

// childLogPath is where the output of one workflow's engine child is written.
func childLogPath(root, workflow string) string {
	return filepath.Join(root, ".sortie", "logs", workflow+".log")
}

// rotatingLog appends to a file and rotates it by size. It is shared by the
// stdout and stderr copies of one child. Write never fails: the first error
// prints one warning and later output is dropped, so the child and its other
// destinations are unaffected.
type rotatingLog struct {
	mu     sync.Mutex
	path   string
	limit  int64
	keep   int
	f      *os.File
	size   int64
	failed bool
}

// openRotatingLog rotates an existing file that is already over limit, then
// opens it for appending.
func openRotatingLog(path string, limit int64, keep int) (*rotatingLog, error) {
	l := &rotatingLog{path: path, limit: limit, keep: keep}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > limit {
		if err := l.shift(); err != nil {
			return nil, err
		}
	}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// shift renames path to path.1, path.1 to path.2 and so on, dropping the
// oldest.
func (l *rotatingLog) shift() error {
	_ = os.Remove(fmt.Sprintf("%s.%d", l.path, l.keep))
	for i := l.keep - 1; i >= 1; i-- {
		if err := os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *rotatingLog) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	l.f = nil
	if err := l.shift(); err != nil {
		return err
	}
	return l.open()
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return len(p), nil
	}
	err := error(nil)
	if l.size > 0 && l.size+int64(len(p)) > l.limit {
		err = l.rotate()
	}
	if err == nil && l.f != nil {
		var n int
		n, err = l.f.Write(p)
		l.size += int64(n)
	}
	if err != nil {
		if !l.failed {
			l.failed = true
			fmt.Fprintf(os.Stderr, "sortie-loop: child log %s: %v\n", l.path, err)
		}
		if l.f != nil {
			_ = l.f.Close()
			l.f = nil
		}
	}
	return len(p), nil
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
