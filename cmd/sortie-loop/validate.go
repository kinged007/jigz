package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kinged007/sortie-loop/internal/config"
	"github.com/kinged007/sortie-loop/internal/engine"
	"github.com/kinged007/sortie-loop/internal/plan"
)

// validateCheck is one validation result.
type validateCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// collectValidateChecks runs repo-config checks and `sortie validate` on
// each workflow. It returns the checks, the workflow file list, and warnings.
func collectValidateChecks(root string) ([]validateCheck, []string, []string) {
	var checks []validateCheck
	var warnings []string

	cfg, err := config.Load(root)
	if err != nil {
		checks = append(checks, validateCheck{Name: "config", OK: false, Error: err.Error()})
		// Without a loaded config, the remaining repo checks cannot run;
		// still try workflow validation.
		wfChecks, files := workflowValidateChecks(root, nil)
		checks = append(checks, wfChecks...)
		return checks, files, warnings
	}
	checks = append(checks, validateCheck{Name: "config", OK: true})

	if err := cfg.CheckCleanup(); err != nil {
		checks = append(checks, validateCheck{Name: "cleanup", OK: false, Error: err.Error()})
	} else {
		checks = append(checks, validateCheck{Name: "cleanup", OK: true})
	}
	if err := cfg.CheckBudgets(); err != nil {
		checks = append(checks, validateCheck{Name: "budgets", OK: false, Error: err.Error()})
	} else {
		checks = append(checks, validateCheck{Name: "budgets", OK: true})
	}
	if err := cfg.CheckToken(); err != nil {
		warnings = append(warnings, err.Error())
	}
	for name, b := range cfg.Budgets {
		if b.MaxUSD > 0 {
			warnings = append(warnings, fmt.Sprintf("workflows.%s.budget.max_usd is ignored: the engine records no cost per run", name))
		}
	}

	wfChecks, files := workflowValidateChecks(root, cfg)
	checks = append(checks, wfChecks...)
	return checks, files, warnings
}

func listWorkflows(root string) []loopDef {
	dir := filepath.Join(root, ".sortie", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var loops []loopDef
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "WORKFLOW.") || !strings.HasSuffix(name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(name, "WORKFLOW."), ".md")
		loops = append(loops, loopDef{stem, name})
	}
	return loops
}

func workflowValidateChecks(root string, cfg *config.Config) ([]validateCheck, []string) {
	loops := listWorkflows(root)
	var files []string
	for _, l := range loops {
		files = append(files, l.file)
	}
	if len(loops) == 0 {
		return []validateCheck{{Name: "workflows", OK: false, Error: fmt.Sprintf("no WORKFLOW.*.md files in %s", filepath.Join(root, ".sortie", "workflows"))}}, files
	}

	bin, err := resolveSortieBin()
	if err != nil {
		var checks []validateCheck
		for _, l := range loops {
			checks = append(checks, validateCheck{Name: "workflow:" + l.file, OK: false, Error: err.Error()})
		}
		return checks, files
	}

	var checks []validateCheck
	dir := filepath.Join(root, ".sortie", "workflows")
	for _, l := range loops {
		out, err := runSortieValidate(bin, dir, l, cfg)
		name := "workflow:" + l.file
		if err != nil {
			checks = append(checks, validateCheck{Name: name, OK: false, Error: err.Error(), Output: out})
		} else {
			c := validateCheck{Name: name, OK: true}
			if out != "" {
				c.Output = out
			}
			checks = append(checks, c)
		}
	}
	return checks, files
}

func runSortieValidate(bin, dir string, loop loopDef, cfg *config.Config) (string, error) {
	env := os.Environ()
	if cfg != nil {
		env = append(env, cfg.Env()...)
	} else {
		// No config: supply dummy env so workflow syntax is validated
		// without spurious tracker.api_key errors. Use a writable temp
		// workspace so workspace.root_writable passes.
		fallbackWS := filepath.Join(os.TempDir(), "sortie-loop-validate", "workspaces")
		_ = os.MkdirAll(fallbackWS, 0o755)
		env = append(env,
			"SORTIE_TRACKER_PROJECT=owner/name",
			"SORTIE_TRACKER_API_KEY=test",
			"SORTIE_LOOP_WORKSPACES="+fallbackWS,
		)
	}
	if text, err := os.ReadFile(filepath.Join(dir, loop.file)); err == nil {
		lg := plan.GateLoop(engine.Detect(bin), string(text), env)
		switch {
		case lg.Problem:
			return "", errors.New(lg.Off)
		case lg.Off != "":
			return "disabled: " + lg.Off, nil
		}
		env = append(env, lg.Env...)
	}
	cmd := exec.Command(bin, "validate", loop.file)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return dropRuntimeBlockWarnings(string(out)), err
}

func dropRuntimeBlockWarnings(out string) string {
	lines := strings.Split(out, "\n")
	keep := lines[:0]
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "warning: "); ok {
			if check, msg, ok := strings.Cut(rest, ": "); ok && plan.IsRuntimeBlockWarning(check, msg) {
				continue
			}
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
