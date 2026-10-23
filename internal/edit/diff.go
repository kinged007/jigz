package edit

import (
	"fmt"
	"strings"
)

type op struct {
	kind byte // ' ', '-', '+'
	text string
}

// lineDiff returns the edit script turning a into b, line by line.
func lineDiff(a, b []string) []op {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var out []op
	for _, l := range a[:pre] {
		out = append(out, op{' ', l})
	}
	out = append(out, lcsDiff(ma, mb)...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, op{' ', l})
	}
	return out
}

// lcsDiff diffs the differing middle of two files. Config files are a few
// hundred lines, so the quadratic table is small; a pathological size
// falls back to replacing the whole middle.
func lcsDiff(a, b []string) []op {
	var out []op
	if len(a)*len(b) > 4_000_000 {
		for _, l := range a {
			out = append(out, op{'-', l})
		}
		for _, l := range b {
			out = append(out, op{'+', l})
		}
		return out
	}
	t := make([][]int32, len(a)+1)
	for i := range t {
		t[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, op{' ', a[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			out = append(out, op{'-', a[i]})
			i++
		default:
			out = append(out, op{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, op{'-', a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, op{'+', b[j]})
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// Diff renders a unified diff with three lines of context, empty when the
// texts are equal.
func Diff(name, a, b string) string {
	if a == b {
		return ""
	}
	ops := lineDiff(splitLines(a), splitLines(b))
	const ctx = 3
	near := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for k := max(0, i-ctx); k <= min(len(ops)-1, i+ctx); k++ {
			near[k] = true
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", name, name)
	// Line numbers in a and b at each op index.
	ai, bi := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, o := range ops {
		ai[i+1], bi[i+1] = ai[i], bi[i]
		if o.kind != '+' {
			ai[i+1]++
		}
		if o.kind != '-' {
			bi[i+1]++
		}
	}
	for i := 0; i < len(ops); {
		if !near[i] {
			i++
			continue
		}
		j := i
		for j < len(ops) && near[j] {
			j++
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", ai[i]+1, ai[j]-ai[i], bi[i]+1, bi[j]-bi[i])
		for _, o := range ops[i:j] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.text)
			sb.WriteByte('\n')
		}
		i = j
	}
	return sb.String()
}

// keepBlankLines puts back the blank lines the YAML encoder drops, so an
// edit does not show as a change to every gap between sections. In each
// changed block, the blank lines that opened or closed the removed text
// are kept around the replacement.
func keepBlankLines(orig, out string) string {
	ops := lineDiff(splitLines(orig), splitLines(out))
	var lines []string
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			lines = append(lines, ops[i].text)
			i++
			continue
		}
		var dels, adds []string
		for ; i < len(ops) && ops[i].kind != ' '; i++ {
			if ops[i].kind == '-' {
				dels = append(dels, ops[i].text)
			} else {
				adds = append(adds, ops[i].text)
			}
		}
		if len(dels) == len(adds) {
			// The encoder also normalizes the spaces before a line comment.
			for k := range adds {
				if strings.Join(strings.Fields(dels[k]), " ") == strings.Join(strings.Fields(adds[k]), " ") {
					adds[k] = dels[k]
				}
			}
		}
		lead := 0
		for lead < len(dels) && strings.TrimSpace(dels[lead]) == "" {
			lead++
		}
		trail := 0
		for lead+trail < len(dels) && strings.TrimSpace(dels[len(dels)-1-trail]) == "" {
			trail++
		}
		if lead == len(dels) {
			lines = append(lines, adds...)
			lines = append(lines, dels...)
			continue
		}
		lines = append(lines, dels[:lead]...)
		lines = append(lines, adds...)
		lines = append(lines, dels[len(dels)-trail:]...)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
