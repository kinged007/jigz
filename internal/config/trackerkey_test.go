package config

import (
	"strings"
	"testing"
)

func clearChain(t *testing.T) {
	t.Helper()
	for _, k := range []string{"JIG_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "TK_DEFAULT", "TK_PRS"} {
		t.Setenv(k, "")
	}
}

func envValueOf(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val = v
		}
	}
	return val
}

// AC-1
func TestTrackerAPIKeyWinsOverTheChain(t *testing.T) {
	clearChain(t)
	t.Setenv("JIG_TOKEN", "chain-token")
	t.Setenv("GITHUB_TOKEN", "gh-token")
	t.Setenv("TK_DEFAULT", "tracker-token")
	cfg, err := loadWithConfig(t, "token: file-token\ntracker:\n  api_key: {from_env: TK_DEFAULT}\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := cfg.TrackerToken(cfg.TrackerDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValueOf(cfg.Env(tok), "SORTIE_TRACKER_API_KEY"); got != "tracker-token" {
		t.Errorf("SORTIE_TRACKER_API_KEY = %q, want the tracker.api_key value", got)
	}
	if cfg.Token != "chain-token" {
		t.Errorf("chain token = %q, the chain used by other features must stay", cfg.Token)
	}
}

// AC-2
func TestWithoutTrackerAPIKeyTheChainApplies(t *testing.T) {
	clearChain(t)
	t.Setenv("GITHUB_TOKEN", "gh-token")
	cfg, err := loadWithConfig(t, "tracker:\n  kind: github\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := cfg.TrackerToken(cfg.TrackerDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValueOf(cfg.Env(tok), "SORTIE_TRACKER_API_KEY"); got != "gh-token" {
		t.Errorf("SORTIE_TRACKER_API_KEY = %q, want gh-token", got)
	}
	if err := cfg.CheckToken(); err != nil {
		t.Errorf("CheckToken: %v", err)
	}
}

// AC-3, AC-7
func TestUnsetTrackerAPIKeyVariableFailsWithoutFallback(t *testing.T) {
	clearChain(t)
	t.Setenv("JIG_TOKEN", "chain-secret")
	cfg, err := loadWithConfig(t, "tracker:\n  api_key: {from_env: TK_DEFAULT}\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.TrackerToken(cfg.TrackerDefaults); err == nil {
		t.Fatal("TrackerToken: want an error for an unset variable")
	}
	err = cfg.CheckToken()
	if err == nil {
		t.Fatal("CheckToken: want an error, the chain must not be a fallback")
	}
	for _, want := range []string{"TK_DEFAULT", "tracker.api_key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "chain-secret") {
		t.Errorf("error contains a credential: %v", err)
	}
}

// AC-3
func TestSeededTrackerTokenHasAHint(t *testing.T) {
	clearChain(t)
	t.Setenv("JIG_TRACKER_TOKEN", "")
	cfg, err := loadWithConfig(t, "tracker:\n  api_key: {from_env: JIG_TRACKER_TOKEN}\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.CheckToken()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"JIG_TRACKER_TOKEN", "tracker.api_key", "Export", "remove"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// AC-4
func TestWorkEntryAPIKeyAppliesToItsOwnLoop(t *testing.T) {
	clearChain(t)
	t.Setenv("GITHUB_TOKEN", "gh-token")
	t.Setenv("TK_PRS", "prs-token")
	cfg, err := loadWithConfig(t, "agents:\n  a: {runtime: pi}\nwork:\n  issues:\n    agent: a\n  prs:\n    agent: a\n    tracker:\n      api_key: {from_env: TK_PRS}\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	res := cfg.Resolve()
	want := map[string]string{"issues": "gh-token", "prs": "prs-token"}
	for slug, w := range want {
		tok, err := cfg.TrackerToken(res[slug].Tracker)
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if tok != w {
			t.Errorf("%s token = %q, want %q", slug, tok, w)
		}
	}
	if err := cfg.CheckToken(); err != nil {
		t.Errorf("CheckToken: %v", err)
	}
	t.Setenv("TK_PRS", "")
	err = cfg.CheckToken()
	if err == nil || !strings.Contains(err.Error(), "TK_PRS") || !strings.Contains(err.Error(), "tracker.api_key") {
		t.Errorf("unset overlay variable: got %v", err)
	}
}
