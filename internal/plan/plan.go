// Package plan turns a resolved central config into the on-disk state
// one repository needs: its .sortie/config.yaml, its installed
// workflows, and the environment its sortie-loop child runs with.
//
// Validation is layered, and each layer is reported rather than
// swallowed. The config layer is Load, which fails before anything is
// written. The workflow layer is `sortie validate`, run here against
// the files that will actually run, so a bad generated workflow is
// caught before any loop is started rather than by the first issue
// claim. Runtime failures belong to the supervisor, which reports a
// dead child against the repository it belonged to.
package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	config "github.com/kinged007/sortie-loop/internal/fleetconfig"
	"github.com/kinged007/sortie-loop/internal/machine"
	"gopkg.in/yaml.v3"
)

// knobTarget is where a knob is written in the workflow front matter.
// The key's name inside its block is not always the knob's own name:
// polling_interval_ms is interval_ms inside polling:.
type knobTarget struct {
	block string
	key   string
}

var knobTargets = map[string]knobTarget{
	"kind":                  {"agent", "kind"},
	"command":               {"agent", "command"},
	"max_turns":             {"agent", "max_turns"},
	"max_concurrent_agents": {"agent", "max_concurrent_agents"},
	"turn_timeout_ms":       {"agent", "turn_timeout_ms"},
	"read_timeout_ms":       {"agent", "read_timeout_ms"},
	"stall_timeout_ms":      {"agent", "stall_timeout_ms"},
	"stop_grace_ms":         {"agent", "stop_grace_ms"},
	"max_retry_backoff_ms":  {"agent", "max_retry_backoff_ms"},
	"polling_interval_ms":   {"polling", "interval_ms"},
}

// ApplySettings writes the knob values that belong in the workflow files
// rather than in the environment: the model, and anything any workflow
// overrides. A repository with no overrides writes only the model line,
// exactly as before.
//
// The edits are line-level on purpose. A YAML round-trip would drop the
// comments the shipped workflows carry, which is the whole reason the
// other knobs stay on the environment path.
func (p Plan) ApplySettings(r config.Resolved) error {
	files, err := p.workflowFiles(r)
	if err != nil {
		return err
	}
	kind := r.Agent.Kind
	if kind == "" {
		kind = defaultAdapter
	}
	for _, f := range files {
		stem := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "WORKFLOW."), ".md")
		knobs := r.FileSettings(stem)
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		// Always, and before the knobs: dropping a model is the case that
		// leaves the file referring to a variable that is no longer
		// generated, which the engine refuses to start on.
		out, err := pruneModelRefs(raw)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", r.Repo.Repo, filepath.Base(f), err)
		}
		if len(knobs) == 0 {
			if string(out) == string(raw) {
				continue
			}
			if err := os.WriteFile(f, out, 0o644); err != nil {
				return err
			}
			continue
		}
		for key, value := range knobs {
			target, known := knobTargets[key]
			block, fileKey, create := target.block, target.key, false
			switch {
			case key == "model":
				// The engine has no model override, so the file points at
				// a variable. The adapter block is named by the agent
				// kind, and the shipped workflows only carry the one for
				// their own default kind, so this one may need creating.
				block, fileKey, create = kind, "model", true
				value = "$" + r.ModelVar(stem)
			case !known:
				return fmt.Errorf("%s: %s: no front matter target for %q", r.Repo.Repo, filepath.Base(f), key)
			}
			patched, changed, err := patchBlock(out, block, fileKey, value, create)
			if err != nil {
				return fmt.Errorf("%s: %s: %w", r.Repo.Repo, filepath.Base(f), err)
			}
			if changed {
				out = patched
			}
		}
		if string(out) == string(raw) {
			continue
		}
		if err := os.WriteFile(f, out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// modelRef is the variable the model line points at, so the model reaches
// the adapter as a value rather than a literal. No shipped workflow
// mentions it, so a line naming one is unambiguously central's.
const modelRef = "$SORTIE_CENTRAL_MODEL"

// modelUnset is what every shipped workflow carries for a model that is
// not configured, so restoring it makes removing a model reversible.
//
// ponytail: a hand-edited model line is lost when the model is removed,
// because central cannot tell its own value from an operator's.
const modelUnset = `""`

// pruneModelRefs undoes the model line central wrote, for when the model
// is no longer configured. Left behind, it fails validation twice over:
// the reference dangles because the variable is no longer generated, and
// an adapter block central created is a top-level key the engine does not
// recognise once the agent kind is no longer that adapter.
//
// A block whose only content is central's own line, and whose name is not
// the default adapter's, is one central created: the shipped workflows
// carry a block for their own adapter and no other. It is dropped, because
// an emptied block would still be an unrecognised top-level key. The
// default adapter's block is shipped and is kept, as is any block holding
// anything else, so only the reference in those is reverted.
func pruneModelRefs(body []byte) ([]byte, error) {
	lines := strings.Split(string(body), "\n")
	start, end, err := frontMatter(lines)
	if err != nil {
		return nil, err
	}
	keep := make([]bool, end-start)
	for i := range keep {
		keep[i] = true
	}
	for _, b := range topLevelBlocks(lines, start, end) {
		own, other := 0, 0
		// The header names the block; it is not content, and counting it
		// would make every block look hand-written.
		for i := b.from + 1; i < b.to; i++ {
			switch classify(lines[i]) {
			case lineOwn:
				own++
			case lineOther:
				other++
			}
		}
		if own == 0 {
			continue
		}
		drop := other == 0 && strings.TrimSuffix(strings.TrimSpace(lines[b.from]), ":") != defaultAdapter
		for i := b.from + 1; i < b.to; i++ {
			if classify(lines[i]) == lineOwn {
				if drop {
					keep[i-start] = false
				} else {
					lines[i] = modelKeyLine(lines[i])
				}
			}
		}
		// The block header goes too, so a dropped block leaves nothing.
		if drop {
			keep[b.from-start] = false
		}
	}
	var out []string
	out = append(out, lines[:start]...)
	for i := start; i < end; i++ {
		if keep[i-start] {
			out = append(out, lines[i])
		}
	}
	out = append(out, lines[end:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// modelKeyLine is the same line with the reference replaced by the
// shipped unset value, keeping the key and any trailing comment.
func modelKeyLine(line string) string {
	name, rest, _ := strings.Cut(line, ":")
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, "$"); i >= 0 {
		if j := strings.Index(rest[i:], " "); j >= 0 {
			rest = modelUnset + rest[i+j:]
		} else {
			rest = modelUnset
		}
	}
	return strings.TrimRight(name, " ") + ": " + rest
}

type lineClass int

const (
	lineBlank lineClass = iota
	lineComment
	lineOwn
	lineOther
)

// classify decides what a front-matter line is, for the purpose of
// working out whether a block is central's.
func classify(line string) lineClass {
	t := strings.TrimSpace(line)
	switch {
	case t == "":
		return lineBlank
	case strings.HasPrefix(t, "#"):
		return lineComment
	case strings.Contains(t, modelRef):
		return lineOwn
	default:
		return lineOther
	}
}

type block struct {
	from, to int
}

// topLevelBlocks returns the spans of the top-level keys in the front
// matter, each covering its header and its indented lines. Keys at column
// zero end the block they follow.
func topLevelBlocks(lines []string, start, end int) []block {
	var out []block
	for i := start + 1; i < end; i++ {
		if isTopLevel(lines[i]) {
			b := block{from: i, to: i + 1}
			for b.to < end && !isTopLevel(lines[b.to]) {
				b.to++
			}
			out = append(out, b)
			i = b.to - 1
		}
	}
	return out
}

func isTopLevel(line string) bool {
	return line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") &&
		(strings.HasPrefix(line, "-") || strings.Contains(line, ":"))
}

// patchBlock sets key to value inside the named top-level front-matter
// block, replacing the existing line or inserting one after the block
// header. create adds the block when it is absent, which is how an
// adapter block for a non-default agent kind gets into a workflow that
// ships only the default one. A block that is absent and not created is
// reported rather than guessed: the agent: and polling: blocks are
// load-bearing and their shape is not this function's to invent.
func patchBlock(body []byte, block, key, value string, create bool) ([]byte, bool, error) {
	lines := strings.Split(string(body), "\n")
	start, end, err := frontMatter(lines)
	if err != nil {
		return nil, false, err
	}
	header := -1
	for i := start + 1; i < end; i++ {
		if line := lines[i]; !strings.HasPrefix(line, " ") && strings.HasPrefix(line, block+":") {
			header = i
			break
		}
	}
	if header < 0 {
		if !create {
			return nil, false, fmt.Errorf("no %s: block", block)
		}
		out := make([]string, 0, len(lines)+2)
		out = append(out, lines[:end]...)
		out = append(out, block+":", "  "+key+": "+value)
		out = append(out, lines[end:]...)
		return []byte(strings.Join(out, "\n")), true, nil
	}
	// The block runs to the next top-level key or to the closing fence.
	stop := end
	for i := header + 1; i < end; i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			stop = i
			break
		}
	}
	indent := "  "
	for i := header + 1; i < stop; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		indent = lines[i][:strings.Index(lines[i], key)]
		updated := indent + key + ": " + value
		if lines[i] == updated {
			return body, false, nil
		}
		lines[i] = updated
		return []byte(strings.Join(lines, "\n")), true, nil
	}
	// The key is absent from the block: add it directly under the header.
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:header+1]...)
	out = append(out, indent+key+": "+value)
	out = append(out, lines[header+1:]...)
	return []byte(strings.Join(out, "\n")), true, nil
}

// frontMatter returns the line range of the YAML front matter: the first
// --- fence and the next one.
func frontMatter(lines []string) (int, int, error) {
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "---" {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, errors.New("no front matter (no leading --- fence)")
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return start, i, nil
		}
	}
	return 0, 0, errors.New("unterminated front matter")
}

// modelVarDefault is the adapter block the shipped workflows use when no
// agent kind is configured.
const defaultAdapter = "pi"

// Diagnostics is one repository's validation result. Empty Errors and
// Warnings with Valid true means the repository is ready to start.
type Diagnostics struct {
	Repo     string   `json:"repo"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Plan materializes one repository. Every step is idempotent: running
// it twice leaves the same bytes, so it is safe to re-run on every
// start and from the UI's apply action.
type Plan struct {
	// LoopBin is the sortie-loop binary. Empty resolves it from PATH.
	LoopBin string
	// SortieBin is the sortie engine. Empty resolves it from PATH.
	SortieBin string
}

// LoopBinary returns the sortie-loop binary to drive: the explicit
// setting, then SORTIE_LOOP_BIN, then a binary beside this one, then
// PATH.
func (p Plan) LoopBinary() string {
	if p.LoopBin != "" {
		return p.LoopBin
	}
	if v := os.Getenv("SORTIE_LOOP_BIN"); v != "" {
		return v
	}
	// A binary installed next to this one, which is what the installers
	// produce. Resolving by name alone would fail for anyone who ran
	// sortie-central by absolute path from a shell whose PATH does not
	// include the prefix.
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), "sortie-loop")
		if fileExists(cand) {
			return cand
		}
	}
	return "sortie-loop"
}

// SortieBinPath exposes the resolved engine so a child process can be
// pinned to it.
func (p Plan) SortieBinPath() (string, error) { return p.sortieBin() }

// sortieBin resolves the engine in the same order sortie-loop resolves
// it: an explicit path, the environment, the version-named copy under
// share/sortie-loop, beside the loop binary, then PATH. The share
// directory comes before the last two because the engine lives there
// rather than on PATH: sortie-loop downloads it under share/sortie-loop
// and links it into place during setup, so a machine where the
// installers ran and PATH was never extended still has a perfectly good
// engine that a PATH-only lookup cannot see.
func (p Plan) sortieBin() (string, error) {
	if p.SortieBin != "" {
		return p.SortieBin, nil
	}
	if v := os.Getenv("SORTIE_BIN"); v != "" {
		return v, nil
	}
	// The version-pinned copy the installer downloaded, before any
	// symlink. A symlink beside the binaries may point anywhere: sortie-loop
	// builds that symlink from whatever its own lookup found, which is a
	// PATH hit when one exists, so a machine with several engines can end
	// up with the prefix's sortie running a binary the prefix never
	// installed. Pinning the file itself makes the engine the loops run the
	// one the workflows were validated against.
	if bin := p.versionedSortie(); bin != "" {
		return bin, nil
	}
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), "sortie")
		if fileExists(cand) {
			return cand, nil
		}
	}
	if bin, err := exec.LookPath("sortie"); err == nil {
		return bin, nil
	}
	return "", fmt.Errorf("sortie engine not found; looked for SORTIE_BIN, %s, sortie beside %s and on PATH. "+
		"Rerun sortie-central's install.sh, or set SORTIE_BIN to the engine", p.sortieShareGlob(), sortieSearchDirs())
}

// sortieShareGlob names the places the installer may have put the
// version-named engine, for the error message.
func (p Plan) sortieShareGlob() string {
	dirs := p.shareDirs()
	if len(dirs) == 0 {
		return "the sortie-loop share directory"
	}
	return strings.Join(dirs, " or ") + " (sortie-<version>)"
}

// versionedSortie looks for the version-pinned engine the installer
// downloaded. The version is asked of sortie-loop rather than hardcoded,
// so it cannot drift from the workflows that binary was released for.
//
// Both prefix locations are searched. sortie-loop's own lookup only
// knows ~/.local, so an install made with --prefix elsewhere leaves the
// engine in <prefix>/share and the default path empty; deriving the
// directory from the binaries actually in use is what makes a custom
// prefix work.
func (p Plan) versionedSortie() string {
	want := ""
	if v, err := exec.Command(p.LoopBinary(), "--dump-version").Output(); err == nil {
		want = strings.TrimSpace(string(v))
	}
	for _, dir := range p.shareDirs() {
		if want != "" {
			if cand := filepath.Join(dir, "sortie-"+want); fileExists(cand) {
				return cand
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "sortie-") {
				continue
			}
			if cand := filepath.Join(dir, e.Name()); fileExists(cand) {
				return cand
			}
		}
	}
	return ""
}

// shareDirs are the directories the installer may have put the
// version-named engine in: next to the binaries actually in use, and the
// default under the home directory.
func (p Plan) shareDirs() []string {
	var dirs []string
	add := func(bin string) {
		if bin == "" {
			return
		}
		abs, err := filepath.Abs(bin)
		if err != nil {
			return
		}
		d := filepath.Join(filepath.Dir(filepath.Dir(abs)), "share", "sortie-loop")
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if self, err := os.Executable(); err == nil {
		add(self)
	}
	add(p.LoopBinary())
	if home := homeDir(); home != "" {
		d := filepath.Join(home, ".local", "share", "sortie-loop")
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func sortieSearchDirs() string {
	if self, err := os.Executable(); err == nil {
		return filepath.Dir(self)
	}
	return "the directory holding sortie-central"
}

// homeDir mirrors sortie-loop's, which prefers $HOME over the database
// lookup so a container running as root finds the same prefix the
// installer wrote.
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// configFile is what central writes into each repository's .sortie/.
// Only settings central actually holds are emitted, so every key left
// out keeps sortie-loop's own default: this file is an overlay, not a
// replacement. Labels are never written for the same reason — an
// absent labels: key makes setup use its shipped list.
type configFile struct {
	Repo      string            `yaml:"repo"`
	Milestone string            `yaml:"milestone,omitempty"`
	Assignee  *string           `yaml:"assignee,omitempty"`
	Filters   map[string]string `yaml:"filters,omitempty"`
}

// Apply writes one repository's config.yaml. It is written through a
// temporary file and renamed, so a crash mid-write cannot leave a
// half-written config that sortie-loop would fail to parse on the next
// start.
func Apply(r config.Resolved) error {
	dir := filepath.Join(r.Dir, ".sortie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%s: %w", r.Repo.Repo, err)
	}
	body, err := marshalOverlay(configFile{
		Repo:      r.Repo.Repo,
		Milestone: r.Milestone,
		Assignee:  r.Assignee,
		Filters:   r.Repo.Filters,
	})
	if err != nil {
		return err
	}
	header := "# Written by sortie-central. Edit sortie-central.yaml instead;\n" +
		"# this file is regenerated. Keys left out keep sortie-loop's defaults.\n"
	final := filepath.Join(dir, "config.yaml")
	tmp, err := os.CreateTemp(dir, "config.yaml.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(header + string(body)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

// marshalOverlay encodes the overlay with the two-space indent
// yaml.v3 uses in its own output, so a file central writes looks like
// the one sortie-loop setup writes. The file can be committed when a
// repository points path: at a real checkout, and a human reads it.
func marshalOverlay(v configFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Setup installs the workflows, labels, .gitignore, and PM prompt by
// driving `sortie-loop setup`, which already owns all of that.
//
// Stdin is left empty so setup never blocks on its overwrite prompt: a
// non-interactive stdin declines, which keeps a file central has
// deliberately patched instead of restoring the shipped copy. Setup is
// additive on labels and never deletes one, so running it per start is
// safe.
func (p Plan) Setup(r config.Resolved) error {
	out, err := p.RunSetup(r)
	if err != nil {
		return fmt.Errorf("%s: sortie-loop setup: %w: %s", r.Repo.Repo, err, lastLine(out))
	}
	return nil
}

// RunSetup is Setup that also returns everything the command printed, for
// a caller that shows the output.
func (p Plan) RunSetup(r config.Resolved) (string, error) {
	env, err := r.Env()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(p.LoopBinary(), "setup", r.Dir)
	cmd.Env = append(append(os.Environ(), env...), "SORTIE_LOOP_REPO="+r.Repo.Repo)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	return out.String(), err
}

// LastLine returns the final non-empty line of setup's output, which is
// where its fatal message lands.
func lastLine(s string) string {
	var last string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			last = t
		}
	}
	return last
}

// Validate runs `sortie validate` over the workflows this repository
// will start, in the same environment the child will run in, so the
// result reflects the start rather than the file on disk alone.
//
// It reports every problem it finds instead of stopping at the first,
// and never treats warnings as failures: the engine warns on an unset
// $VAR in an extension block, which is a legitimate state.
func (p Plan) Validate(r config.Resolved) (Diagnostics, error) {
	d := Diagnostics{Repo: r.Repo.Repo}
	bin, err := p.sortieBin()
	if err != nil {
		return d, err
	}
	env, err := r.ValidateEnv()
	if err != nil {
		return d, err
	}
	files, err := p.workflowFiles(r)
	if err != nil {
		return d, err
	}
	if len(files) == 0 {
		return d, fmt.Errorf("%s: no workflows installed under %s", r.Repo.Repo, filepath.Join(r.Dir, ".sortie", "workflows"))
	}
	d.Valid = true
	for _, f := range files {
		res, err := p.validateFile(bin, env, f)
		if err != nil {
			return d, fmt.Errorf("%s: %w", r.Repo.Repo, err)
		}
		if !res.Valid {
			d.Valid = false
		}
		for _, e := range res.Errors {
			d.Errors = append(d.Errors, filepath.Base(f)+": "+e.Message)
		}
		for _, w := range res.Warnings {
			d.Warnings = append(d.Warnings, filepath.Base(f)+": "+w.Message)
		}
	}
	return d, nil
}

// Issue is one finding of `sortie validate`. Check is the dotted path of
// the setting it concerns, such as config.dispatch.rules[0].agent.
type Issue struct {
	Check   string `json:"check"`
	Message string `json:"message"`
}

// IsRuntimeBlockWarning reports whether an engine warning only says that
// the workflow carries the adapter block of a runtime other than the active
// one. The shipped workflows hold one block per runtime and the engine reads
// only the block named by agent.kind.
func IsRuntimeBlockWarning(check, message string) bool {
	if check != "unknown_key" {
		return false
	}
	for _, rt := range machine.Runtimes {
		if message == fmt.Sprintf("%s: unknown top-level key %q", rt, rt) {
			return true
		}
	}
	return false
}

// WithoutRuntimeBlockWarnings drops the warnings IsRuntimeBlockWarning matches.
func (r FileResult) WithoutRuntimeBlockWarnings() FileResult {
	var keep []Issue
	for _, w := range r.Warnings {
		if !IsRuntimeBlockWarning(w.Check, w.Message) {
			keep = append(keep, w)
		}
	}
	r.Warnings = keep
	return r
}

// FileResult is the engine's verdict on one workflow file.
type FileResult struct {
	Valid    bool    `json:"valid"`
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}

// ValidateFile runs `sortie validate` on one file in the environment the
// repository's child would have. The file may be a candidate that is not
// installed, as long as it sits where its relative paths resolve.
func (p Plan) ValidateFile(r config.Resolved, file string) (FileResult, error) {
	bin, err := p.sortieBin()
	if err != nil {
		return FileResult{}, err
	}
	env, err := r.ValidateEnv()
	if err != nil {
		return FileResult{}, err
	}
	return p.validateFile(bin, env, file)
}

func (p Plan) validateFile(bin string, env []string, f string) (FileResult, error) {
	cmd := exec.Command(bin, "validate", "--format", "json", f)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// A non-zero exit is a validation failure, not a tool
		// failure: the diagnostics below say why. Only a missing
		// or unrunnable binary lands here without output.
		if stdout.Len() == 0 {
			return FileResult{}, fmt.Errorf("%s validate %s: %w: %s", bin, filepath.Base(f), err, lastLine(stderr.String()))
		}
	}
	var out FileResult
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return FileResult{}, fmt.Errorf("%s validate %s: unreadable output: %w", bin, filepath.Base(f), err)
	}
	return out.WithoutRuntimeBlockWarnings(), nil
}

// workflowFiles lists the workflow files this repository starts: the
// configured selection, or every installed workflow when none is
// configured.
func (p Plan) workflowFiles(r config.Resolved) ([]string, error) {
	dir := filepath.Join(r.Dir, ".sortie", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	keep := map[string]bool{}
	for _, w := range r.Workflows {
		keep[w] = true
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "WORKFLOW.") || !strings.HasSuffix(n, ".md") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(n, "WORKFLOW."), ".md")
		if len(keep) > 0 && !keep[stem] {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out, nil
}

// TrackerKind is tracker.kind from a workflow's front matter, "" when the
// file or the key is missing or unreadable.
func TrackerKind(r config.Resolved, workflow string) string {
	raw, err := os.ReadFile(filepath.Join(r.Dir, ".sortie", "workflows", "WORKFLOW."+workflow+".md"))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	start, end, err := frontMatter(lines)
	if err != nil {
		return ""
	}
	var doc struct {
		Tracker struct {
			Kind string `yaml:"kind"`
		} `yaml:"tracker"`
	}
	if yaml.Unmarshal([]byte(strings.Join(lines[start+1:end], "\n")), &doc) != nil {
		return ""
	}
	return doc.Tracker.Kind
}
