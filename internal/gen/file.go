package gen

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Status is how a document on disk compares to freshly generated
// content. Clean means the file matches what generation would write;
// Drift means a managed document has a hand edit; Unmanaged means the
// entry leaves the document alone.
type Status int

const (
	Clean Status = iota
	Drift
	Unmanaged
)

// Check compares the document at path against the text generation
// would write and reports whether the file is clean, has a hand edit,
// or belongs to an unmanaged entry. A missing file for a managed entry
// is drift; for an unmanaged entry it is unmanaged.
func Check(path string, managed bool, render func() ([]byte, error)) (Status, []byte, []byte, error) {
	disk, err := os.ReadFile(path)
	switch {
	case err == nil:
		if !managed {
			return Unmanaged, disk, nil, nil
		}
		want, err := render()
		if err != nil {
			return Drift, disk, nil, err
		}
		if digestOf(disk) == digestOf(want) {
			return Clean, disk, want, nil
		}
		return Drift, disk, want, nil
	case errors.Is(err, os.ErrNotExist):
		if !managed {
			return Unmanaged, nil, nil, nil
		}
		want, err := render()
		if err != nil {
			return Drift, nil, nil, err
		}
		return Drift, nil, want, nil
	default:
		return Drift, nil, nil, err
	}
}

// digestOf is the sha256 of the document with the marker line removed.
func digestOf(body []byte) string { return digest(body) }

// AtomicWrite replaces path with text, keeping the previous copy as
// <path>.bak when one exists. The new file appears at its final name
// in one rename.
func AtomicWrite(path string, text []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jigz-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(text); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if _, err := os.Stat(path); err == nil {
		bak := path + ".bak"
		os.Remove(bak)
		if err := os.Rename(path, bak); err != nil {
			os.Remove(tmpName)
			return err
		}
	}
	return os.Rename(tmpName, path)
}

// DocPath returns the document path for a loop in the repository root.
func DocPath(root, loop string) string {
	return filepath.Join(root, ".jigz", "workflows", "WORKFLOW."+loop+".md")
}

// Managed reads the marker from front matter. A document whose first
// front-matter line carries MarkerPrefix is generated; anything else is
// a hand-owned document.
func Managed(text []byte) bool {
	for _, l := range bytes.Split(text, []byte("\n")) {
		if bytes.Equal(bytes.TrimSpace(l), []byte("---")) {
			continue
		}
		return bytes.HasPrefix(bytes.TrimSpace(l), []byte(MarkerPrefix))
	}
	return false
}

// Manifest describes what a document carries, for status pages and for
// managed: false entries that Jigz only reads. It parses the front
// matter without running the engine.
func Manifest(text []byte) (trackerKind string, ruleAgents []string, err error) {
	front, ok := frontMatter(text)
	if !ok {
		return "", nil, fmt.Errorf("gen: no front matter")
	}
	// The blocks are scanned by indentation, which is how the shipped
	// and generated documents are laid out.
	var current, parent string
	indents := map[string]int{}
	for _, raw := range bytes.Split(front, []byte("\n")) {
		line := string(bytes.TrimRight(raw, " \t\r"))
		stripped := strings.TrimLeft(line, " ")
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		indent := len(line) - len(stripped)
		key, _, found := strings.Cut(stripped, ":")
		if !found || strings.HasPrefix(stripped, "- ") {
			continue
		}
		top := indent == 0
		if top {
			current = strings.TrimSpace(key)
			parent = ""
			continue
		}
		switch current {
		case "tracker":
			if strings.TrimSpace(key) == "kind" {
				trackerKind = unquote(valueOf(stripped))
			}
		case "dispatch":
			if indent == 2 && strings.TrimSpace(key) == "rules" {
				parent = "rules"
				continue
			}
			if parent == "rules" && strings.TrimSpace(key) == "agent" {
				ruleAgents = append(ruleAgents, unquote(valueOf(stripped)))
			}
		}
		_ = indents
	}
	return trackerKind, ruleAgents, nil
}

func valueOf(line string) string {
	_, v, _ := strings.Cut(line, ":")
	return strings.TrimSpace(v)
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func frontMatter(text []byte) ([]byte, bool) {
	rest := text
	if !bytes.HasPrefix(rest, []byte("---\n")) {
		return nil, false
	}
	rest = rest[len("---\n"):]
	i := bytes.Index(rest, []byte("\n---"))
	if i < 0 {
		return nil, false
	}
	return rest[:i], true
}
