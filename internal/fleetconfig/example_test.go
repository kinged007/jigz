package config

import (
	"path/filepath"
	"testing"
)

// The example config is documentation, so it has to keep loading and
// keep behaving the way its comments claim.
func TestExampleConfigLoadsAndDocumentsPerWorkflow(t *testing.T) {
	c, err := Load(filepath.Join("testdata", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	var r Resolved
	for _, x := range all {
		if x.Repo.Repo == "owner/eta" {
			r = x
		}
	}
	if r.Repo.Repo == "" {
		t.Fatal("owner/eta is not in the example config")
	}
	env := envMap(t, mustEnv(t, r))
	if _, ok := env["SORTIE_AGENT_MAX_CONCURRENT_AGENTS"]; ok {
		t.Error("an overridden knob is in the environment, where it would silently beat the override")
	}
	if got := env["SORTIE_LOOP_ONLY"]; got != "plan,build,review" {
		t.Errorf("SORTIE_LOOP_ONLY = %q", got)
	}
	if env["SORTIE_CENTRAL_MODEL"] != "gpt-5" {
		t.Errorf("SORTIE_CENTRAL_MODEL = %q, want gpt-5", env["SORTIE_CENTRAL_MODEL"])
	}
	if env["SORTIE_CENTRAL_MODEL_REVIEW"] != "opus" {
		t.Errorf("SORTIE_CENTRAL_MODEL_REVIEW = %q, want opus", env["SORTIE_CENTRAL_MODEL_REVIEW"])
	}
	// The overridden workflow is heavier; the other two keep the
	// repository value.
	for _, tc := range []struct{ wf, want string }{
		{"plan", "2"},
		{"build", "8"},
		{"review", "2"},
	} {
		if got := r.FileSettings(tc.wf)["max_concurrent_agents"]; got != tc.want {
			t.Errorf("%s max_concurrent_agents = %q, want %q", tc.wf, got, tc.want)
		}
	}
	// review names its own model variable because one file can name
	// only one; build shares the repository's.
	if got := r.ModelVar("review"); got != "SORTIE_CENTRAL_MODEL_REVIEW" {
		t.Errorf("ModelVar(review) = %q", got)
	}
	if got := r.ModelVar("build"); got != "SORTIE_CENTRAL_MODEL" {
		t.Errorf("ModelVar(build) = %q, want the shared variable", got)
	}
}
