package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

const promptsCheckConfig = `work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
      - { name: custom, match: { labels: [agent:custom] }, prompt: prompts/custom.md }
`

func TestPromptsCheckFailsOnMissingAndStubPrompts(t *testing.T) {
	root := writeGenConfig(t, genBase()+promptsCheckConfig)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".jigz", "workflows", "prompts")
	c := promptsCheck(root, cfg)
	if c.OK || !strings.Contains(c.Error, "rule build") || !strings.Contains(c.Error, "rule custom") || !strings.Contains(c.Error, "missing") {
		t.Fatalf("missing prompts passed: %+v", c)
	}

	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensurePrompts(root, docs); err != nil {
		t.Fatal(err)
	}
	c = promptsCheck(root, cfg)
	if c.OK || strings.Contains(c.Error, "rule build") || !strings.Contains(c.Error, "rule custom") || !strings.Contains(c.Error, filepath.Join(dir, "custom.md")) || !strings.Contains(c.Error, "stub") {
		t.Fatalf("stub prompt result: %+v", c)
	}

	if err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte("# custom\n\nTriage the issue.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := promptsCheck(root, cfg); !c.OK {
		t.Fatalf("edited prompts failed: %+v", c)
	}
}

func TestPromptsCheckSkipsUnmanagedEntries(t *testing.T) {
	root := writeGenConfig(t, genBase()+strings.Replace(promptsCheckConfig, "  issues:\n", "  issues:\n    managed: false\n", 1))
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c := promptsCheck(root, cfg); !c.OK {
		t.Fatalf("unmanaged entry checked: %+v", c)
	}
}
