// Package source lists the loops a supervisor runs and the root that holds
// each loop's files. Files reads one repo checkout; Hub writes the roots of
// a connected daemon from the hub config.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type Loop struct {
	Project  string // owner/name
	Workflow string // WORKFLOW.<name>.md stem
	Root     string // holds .jigz/config.yaml and .jigz/workflows/
	// Hash covers the files under Root that the loop reads. The engine
	// reads the query filter only at start, so a loop whose Hash differs
	// from the one it was started with needs a restart.
	Hash string
}

type Source interface {
	Loops(ctx context.Context) ([]Loop, error)
}

// WorkflowFile is the name of a workflow's file in .jigz/workflows/.
func WorkflowFile(workflow string) string { return "WORKFLOW." + workflow + ".md" }

// hashFiles hashes file contents keyed by path relative to <root>/.jigz.
func hashFiles(files map[string][]byte) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "\x00" + strconv.Itoa(len(files[k])) + "\x00"))
		h.Write(files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// readTree adds every regular file under dir to files, keyed by prefix
// plus its slash-separated path below dir. A missing dir adds nothing.
func readTree(files map[string][]byte, dir, prefix string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files[prefix+filepath.ToSlash(rel)] = b
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
