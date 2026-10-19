// Package edit is the pipeline every editor of a config file goes through:
// build a candidate from the current file, validate it, show a diff, check
// the file did not change meanwhile, back it up, write it, make it live,
// and restore the backup when making it live fails.
package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kinged007/sortie-loop/internal/store"
)

// KeepBackups is how many backups of one file are kept.
const KeepBackups = 20

// Target is one config file. Scope is "central" or the project slug.
type Target struct {
	Scope string
	Name  string
	Path  string
}

// Report is the outcome of validating a candidate.
type Report struct {
	Errors   []Problem
	Warnings []string
}

func (r Report) OK() bool { return len(r.Errors) == 0 }

// Applier makes a written file take effect and confirms it did. The string
// Apply returns says how, and is shown in the report. Apply is called again
// after a rollback, with the restored file in place.
type Applier interface {
	Apply(t Target) (string, error)
}

// Build derives a candidate from the current file. Problems it returns
// are shown against form fields and stop the edit.
type Build func(current string) (string, []Problem)

// Pipeline holds what the steps share.
type Pipeline struct {
	// Dir is the backup root, <state_dir>/backups.
	Dir   string
	Store *store.Store
	// Validate checks a candidate. A non-nil error means the check could
	// not run, which blocks the edit like a failed check does.
	Validate func(t Target, candidate string) (Report, error)
	Applier  Applier
	Now      func() time.Time

	// mu serializes Apply and Restore, so two edits cannot interleave
	// their hash check and write.
	mu sync.Mutex
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Hash is the content hash an edit form carries.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Preview is the result of the validate and diff steps.
type Preview struct {
	Current, Candidate string
	Hash               string
	Diff               string
	Report             Report
	// Stale is set when the file changed since the form was loaded.
	Stale bool
}

// Preview builds and validates a candidate without writing anything.
// hash is the content hash the form was loaded with.
func (p *Pipeline) Preview(t Target, hash string, build Build) (Preview, error) {
	raw, err := os.ReadFile(t.Path)
	if err != nil {
		return Preview{}, err
	}
	pv := Preview{Current: string(raw), Hash: Hash(string(raw))}
	pv.Stale = hash != pv.Hash
	cand, probs := build(pv.Current)
	if len(probs) > 0 {
		pv.Report.Errors = probs
		return pv, nil
	}
	pv.Candidate = cand
	pv.Diff = Diff(t.Name, pv.Current, cand)
	if pv.Diff == "" {
		return pv, nil
	}
	pv.Report, err = p.Validate(t, cand)
	return pv, err
}

// Step is one line of an apply report.
type Step struct {
	Name, Msg string
	OK        bool
}

// Outcome is the result of Apply or Restore.
type Outcome struct {
	Steps []Step
	// Applied: the new file is installed and live.
	Applied bool
	// RolledBack: the apply failed and the previous file is installed and
	// live again.
	RolledBack bool
	// Restored: the previous file is back on disk, whether or not it
	// could be made live again.
	Restored bool
	// Conflict: the file changed since the form was loaded, nothing was written.
	Conflict bool
	// NoChange: the candidate equals the current file, nothing was written.
	NoChange bool
	// Report holds the validation errors that stopped the edit.
	Report Report
	// BackupID is the backup taken before the write, 0 if none.
	BackupID int64
}

func (o *Outcome) step(name, msg string, ok bool) { o.Steps = append(o.Steps, Step{name, msg, ok}) }

// Apply runs the whole pipeline for a form post. It checks the hash,
// rebuilds and re-validates the candidate against the file as it is now,
// backs up, writes and applies. actor is recorded in the audit log.
func (p *Pipeline) Apply(t Target, hash string, build Build, actor string) (Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pv, err := p.Preview(t, hash, build)
	var out Outcome
	switch {
	case err != nil:
		return out, err
	case pv.Stale:
		out.Conflict = true
		return out, nil
	case !pv.Report.OK():
		out.Report = pv.Report
		return out, nil
	case pv.Diff == "":
		out.NoChange = true
		return out, nil
	}
	out.step("Validate", "the candidate is valid", true)
	return p.install(t, pv.Current, pv.Candidate, actor, "edit_apply", out)
}

// Restore puts a backup back, through the same backup, apply and rollback
// steps. The file it replaces is backed up first, so a restore can be undone.
func (p *Pipeline) Restore(id int64, actor string) (Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out Outcome
	b, err := p.Store.Backup(id)
	if err != nil {
		return out, fmt.Errorf("backup %d: %w", id, err)
	}
	old, err := os.ReadFile(b.BackupPath)
	if err != nil {
		return out, err
	}
	if Hash(string(old)) != b.SHA256 {
		return out, fmt.Errorf("backup %d does not match its recorded hash and was not restored", id)
	}
	t := Target{Scope: b.Scope, Name: filepath.Base(b.Path), Path: b.Path}
	cur, err := os.ReadFile(t.Path)
	if err != nil {
		return out, err
	}
	rep, err := p.Validate(t, string(old))
	if err != nil {
		return out, err
	}
	if !rep.OK() {
		out.Report = rep
		return out, nil
	}
	out.step("Validate", "the backup is valid", true)
	return p.install(t, string(cur), string(old), actor, "edit_restore", out)
}

func (p *Pipeline) install(t Target, current, candidate, actor, action string, out Outcome) (Outcome, error) {
	b, err := p.backup(t, current, actor)
	if err != nil {
		return out, err
	}
	out.BackupID = b
	out.step("Backup", fmt.Sprintf("backup %d of the current file", b), true)
	if err := writeAtomic(t.Path, candidate); err != nil {
		return out, err
	}
	out.step("Write", "the file is written", true)
	how, err := p.Applier.Apply(t)
	if err == nil {
		out.step("Apply", "the change is live"+how, true)
		out.Applied = true
		p.audit(actor, action, t, "ok")
		return out, nil
	}
	out.step("Apply", err.Error(), false)
	if werr := writeAtomic(t.Path, current); werr != nil {
		out.step("Rollback", "the previous file could not be restored: "+werr.Error(), false)
		p.audit(actor, "edit_rollback", t, "failed: "+werr.Error())
		return out, nil
	}
	out.Restored = true
	if _, aerr := p.Applier.Apply(t); aerr != nil {
		out.step("Rollback", "the previous file is back on disk but is not live: "+aerr.Error(), false)
		p.audit(actor, "edit_rollback", t, "restored, apply failed: "+aerr.Error())
		return out, nil
	}
	out.step("Rollback", "the previous file is restored and live", true)
	out.RolledBack = true
	p.audit(actor, "edit_rollback", t, "ok after: "+err.Error())
	return out, nil
}

func (p *Pipeline) audit(actor, action string, t Target, result string) {
	if p.Store != nil {
		_ = p.Store.AddAudit(p.now(), actor, action, t.Scope, t.Path, result)
	}
}

// backup copies the current content to <Dir>/<scope>/<name>.<timestamp>,
// records it, and prunes the file's older backups.
func (p *Pipeline) backup(t Target, content, actor string) (int64, error) {
	if p.Store == nil {
		return 0, errors.New("no store to record backups in")
	}
	dir := filepath.Join(p.Dir, filepath.FromSlash(t.Scope))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	now := p.now()
	var f *os.File
	var path string
	for n := 0; ; n++ {
		path = filepath.Join(dir, t.Name+"."+now.UTC().Format("20060102T150405.000"))
		if n > 0 {
			path += fmt.Sprintf("-%d", n)
		}
		var err error
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return 0, err
		}
	}
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return 0, werr
	}
	id, err := p.Store.AddBackup(now, store.Backup{Scope: t.Scope, Path: t.Path, BackupPath: path, SHA256: Hash(content), Actor: actor})
	if err != nil {
		os.Remove(path)
		return 0, err
	}
	old, err := p.Store.PruneBackups(t.Path, KeepBackups)
	if err != nil {
		return id, err
	}
	for _, f := range old {
		os.Remove(f)
	}
	return id, nil
}

// writeAtomic replaces a file through a temporary file in its directory,
// keeping its permissions, so a crash never leaves half a file.
func writeAtomic(path, content string) error {
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// BuildDoc makes a Build that edits the whole file as a YAML document.
func BuildDoc(edit func(root *Doc) []Problem) Build {
	return func(current string) (string, []Problem) {
		d, err := ParseDoc(current)
		if err != nil {
			return "", []Problem{{Msg: "the current file is not valid YAML: " + err.Error()}}
		}
		if probs := edit(d); len(probs) > 0 {
			return "", probs
		}
		out, err := d.String()
		if err != nil {
			return "", []Problem{{Msg: err.Error()}}
		}
		return out, nil
	}
}

// BuildFront makes a Build that edits only the front matter of a workflow
// file, leaving everything after the closing fence byte for byte.
func BuildFront(edit func(root *Doc) []Problem) Build {
	return func(current string) (string, []Problem) {
		front, rest, err := SplitFrontMatter(current)
		if err != nil {
			return "", []Problem{{Msg: err.Error()}}
		}
		out, probs := BuildDoc(edit)(front)
		if len(probs) > 0 {
			return "", probs
		}
		return JoinFrontMatter(out, rest), nil
	}
}

// Raw makes a Build for the advanced tab: the posted text replaces the
// file. A workflow's raw text is its front matter only, so the body stays.
func Raw(text string, frontOnly bool) Build {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return func(current string) (string, []Problem) {
		if !frontOnly {
			return text, nil
		}
		_, rest, err := SplitFrontMatter(current)
		if err != nil {
			return "", []Problem{{Msg: err.Error()}}
		}
		if _, err := ParseDoc(text); err != nil {
			return "", []Problem{{Msg: "the front matter is not valid YAML: " + err.Error()}}
		}
		return JoinFrontMatter(text, rest), nil
	}
}
