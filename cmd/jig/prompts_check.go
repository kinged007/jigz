package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kinged007/jigz/internal/config"
)

// stubPromptText is the body line of a prompt that ensurePrompts creates
// when no shipped prompt has the same name. validate fails while a rule
// prompt still holds it.
const stubPromptText = "Fill in the prompt for this rule."

func stubPrompt(name string) []byte {
	return []byte("# " + name + "\n\n" + stubPromptText + "\n")
}

// promptsCheck fails when a rule prompt of a managed work entry is
// missing under .jigz/workflows or is still the stub that generation
// writes.
func promptsCheck(root string, cfg *config.Config) validateCheck {
	resolved := cfg.Resolve()
	var problems []string
	for _, slug := range sortedKeys(resolved) {
		res := resolved[slug]
		if !res.Managed {
			continue
		}
		for _, r := range res.Rules {
			clean := strings.TrimPrefix(r.Prompt, "./")
			if clean == "" || filepath.IsAbs(clean) || strings.Contains(clean, "..") {
				continue
			}
			path := filepath.Join(root, ".jigz", "workflows", clean)
			body, err := os.ReadFile(path)
			switch {
			case err != nil:
				problems = append(problems, fmt.Sprintf("work.%s rule %s: prompt %s is missing", slug, r.Name, path))
			case string(body) == string(stubPrompt(filepath.Base(clean))):
				problems = append(problems, fmt.Sprintf("work.%s rule %s: prompt %s is still the unedited stub", slug, r.Name, path))
			}
		}
	}
	if len(problems) > 0 {
		return validateCheck{Name: "prompts", OK: false, Error: strings.Join(problems, "; ")}
	}
	return validateCheck{Name: "prompts", OK: true}
}
