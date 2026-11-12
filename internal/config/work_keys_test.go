package config

import (
	"strings"
	"testing"
)

func TestWorkMaxSessionsAndPartials(t *testing.T) {
	const head = "agents:\n  a: { runtime: pi }\nwork:\n  w:\n    managed: false\n    query_filter: x\n    agent: a\n"
	cfg, err := loadWithConfig(t, head+"    max_sessions: 4\n    partials: [prompts/partials/*.md, prompts/shared.md]\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	res := cfg.Resolve()["w"]
	if res.MaxSessions != 4 || len(res.Partials) != 2 || res.Partials[0] != "prompts/partials/*.md" {
		t.Errorf("resolved = %+v", res)
	}
	cases := []struct{ name, body, want string }{
		{"negative", "    max_sessions: -1\n", "work.w.max_sessions"},
		{"empty", "    partials: [\"\"]\n", "work.w.partials[0]"},
		{"outside prompts", "    partials: [partials/a.md]\n", "inside prompts/"},
		{"directory only", "    partials: [prompts/]\n", "inside prompts/"},
		{"absolute", "    partials: [/abs/a.md]\n", "inside prompts/"},
		{"parent", "    partials: [prompts/../a.md]\n", "\"..\""},
		{"quote", "    partials: ['prompts/a\"b.md']\n", "quotes"},
	}
	for _, c := range cases {
		if _, err := loadWithConfig(t, head+c.body, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to contain %q", c.name, err, c.want)
		}
	}
}
