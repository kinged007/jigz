package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Name is a page or section the dashboard can hide, with a one-line
// description.
type Name struct{ Name, Doc string }

// HidePages are the dashboard pages that can be hidden. A hidden page is
// removed from the sidebar and from every link, and each route that serves
// it, including its JSON and CSV forms, answers 404.
var HidePages = []Name{
	{"operations", "Operations page, its live fragment, GET /api/v1/fleet and GET /api/v1/metrics"},
	{"runs", "Runs list, CSV export, run detail pages and GET /api/v1/runs"},
	{"questions", "Questions page and GET /api/v1/questions"},
	{"usage", "Usage page and GET /api/v1/usage"},
	{"workflows", "Workflows page"},
	{"projects", "Projects list, project detail pages and GET /api/v1/projects"},
	{"agents", "Agents page"},
	{"settings", "Settings page"},
	{"backups", "Backups page and the backups list on project pages"},
	{"logs", "Child log page of a workflow"},
}

// HideSections are parts of pages that can be hidden while the page stays.
// A route that serves only a section answers 404 when the section is hidden.
var HideSections = []Name{
	{"agent_log", "Agent event log on the run detail page"},
	{"log_tail", "Log tail on the run detail page and the endpoint that serves it"},
	{"audit_log", "Audit log on Operations and project pages and GET /api/v1/audit"},
	{"effective_config", "Effective configuration table on project pages"},
}

// Hide lists what the dashboard hides.
type Hide struct {
	Pages    []string `yaml:"pages"`
	Sections []string `yaml:"sections"`
}

// Dashboard holds the dashboard settings of the machine file.
type Dashboard struct {
	Hide Hide `yaml:"hide"`
}

func (d Dashboard) validate() error {
	if err := checkNames("dashboard.hide.pages", d.Hide.Pages, HidePages); err != nil {
		return err
	}
	return checkNames("dashboard.hide.sections", d.Hide.Sections, HideSections)
}

func checkNames(key string, got []string, valid []Name) error {
	for _, g := range got {
		if !slices.ContainsFunc(valid, func(n Name) bool { return n.Name == g }) {
			names := make([]string, len(valid))
			for i, n := range valid {
				names[i] = n.Name
			}
			return fmt.Errorf("%s: %q is not a known name (use %s)", key, g, strings.Join(names, ", "))
		}
	}
	return nil
}

// LoadDashboard reads only the dashboard block of the machine file, so a
// problem in the hours or pool settings does not stop the dashboard from
// hiding what it is told to hide. The block itself is read strictly: a
// misspelled key is an error and not an empty list. A missing file gives
// the zero Dashboard.
func LoadDashboard(path string) (Dashboard, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Dashboard{}, nil
	}
	if err != nil {
		return Dashboard{}, err
	}
	var doc struct {
		Dashboard yaml.Node `yaml:"dashboard"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Dashboard{}, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Dashboard.IsZero() {
		return Dashboard{}, nil
	}
	block, err := yaml.Marshal(&doc.Dashboard)
	if err != nil {
		return Dashboard{}, fmt.Errorf("%s: %w", path, err)
	}
	var d Dashboard
	dec := yaml.NewDecoder(bytes.NewReader(block))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return Dashboard{}, fmt.Errorf("%s: dashboard: %w", path, err)
	}
	if err := d.validate(); err != nil {
		return Dashboard{}, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}
