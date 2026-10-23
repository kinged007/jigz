package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type validateOutput struct {
	OK            bool            `json:"ok"`
	Checks        []validateCheck `json:"checks"`
	WorkflowFiles []string        `json:"workflow_files"`
	Warnings      []string        `json:"warnings,omitempty"`
}

func runValidate(args []string) {
	dir := "."
	asJSON := false
	for i, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case a == "--repo":
			continue
		case i > 0 && args[i-1] == "--repo":
			continue
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("unknown flag %q", a))
		default:
			dir = a
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal(err)
	}
	out, ok := validateOutputFor(abs, asJSON)
	if asJSON {
		fmt.Println(out)
	} else if out != "" {
		fmt.Println(out)
	}
	if !ok {
		fatal(fmt.Errorf("validation failed"))
	}
}

func validateOutputFor(root string, asJSON bool) (string, bool) {
	checks, files, warnings := collectValidateChecks(root)
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
			break
		}
	}
	if asJSON {
		v := validateOutput{OK: ok, Checks: checks, WorkflowFiles: files, Warnings: warnings}
		data, _ := json.MarshalIndent(v, "", "  ")
		return string(data), ok
	}
	var b strings.Builder
	if len(files) > 0 {
		fmt.Fprintf(&b, "workflows: %s\n", strings.Join(files, ", "))
	}
	for _, c := range checks {
		if c.OK {
			fmt.Fprintf(&b, "PASS %s\n", c.Name)
			if c.Output != "" {
				for _, line := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			}
		} else {
			fmt.Fprintf(&b, "FAIL %s: %s\n", c.Name, c.Error)
			if c.Output != "" {
				for _, line := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			}
		}
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "WARN %s\n", w)
	}
	if ok {
		if len(warnings) > 0 {
			fmt.Fprintln(&b, "ok with warnings")
		} else {
			fmt.Fprintln(&b, "ok")
		}
	}
	return strings.TrimRight(b.String(), "\n"), ok
}
