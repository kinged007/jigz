package main

import (
	"fmt"
	"html"
	"html/template"
	"strings"
)

const (
	boxW, boxH, boxGap = 130, 50, 56
	boxTop             = 12
)

func esc(s string) string { return html.EscapeString(s) }

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// pipeBox is one workflow in the pipeline diagram.
type pipeBox struct {
	Name       string
	Label      string
	NotStarted bool
}

// pipelineSVG draws one column per stage label, left to right, with the
// workflows that share a label stacked in the column. The handoff dot
// follows the last column.
func pipelineSVG(cols [][]pipeBox, handoff string) template.HTML {
	if len(cols) == 0 {
		return ""
	}
	tallest := 0
	for _, c := range cols {
		tallest = max(tallest, len(c))
	}
	colH := tallest*(boxH+14) - 14
	w, h := len(cols)*(boxW+boxGap)+110, boxTop+colH+12
	var g strings.Builder
	var names []string
	for i, col := range cols {
		x := 10 + i*(boxW+boxGap)
		for j, b := range col {
			y := boxTop + j*(boxH+14)
			cls := "box"
			sub := clip(b.Label, 22)
			if b.NotStarted {
				cls, sub = "box idle", "not started"
			}
			fmt.Fprintf(&g, `<g><title>%s %s</title><rect class="%s" x="%d" y="%d" width="%d" height="%d" rx="5"/><text x="%d" y="%d" text-anchor="middle" font-weight="700">%s</text><text class="m" x="%d" y="%d" text-anchor="middle">%s</text></g>`,
				esc(b.Name), esc(b.Label), cls, x, y, boxW, boxH, x+boxW/2, y+20, esc(clip(b.Name, 18)), x+boxW/2, y+36, esc(sub))
			names = append(names, b.Name)
		}
		ay := boxTop + colH/2
		if i < len(cols)-1 {
			tx := x + boxW + boxGap
			fmt.Fprintf(&g, `<path d="M%d %dH%d"/><path class="arrow" d="M%d %dL%d %dL%d %dz"/>`, x+boxW, ay, tx-6, tx-7, ay-4, tx, ay, tx-7, ay+4)
		} else {
			ax := x + boxW
			fmt.Fprintf(&g, `<path d="M%d %dH%d"/><circle cx="%d" cy="%d" r="5" fill="var(--mut)"/>`, ax, ay, ax+22, ax+28, ay)
			if handoff != "" {
				fmt.Fprintf(&g, `<text class="m" x="%d" y="%d">%s</text>`, ax+38, ay+4, esc(clip(handoff, 12)))
			}
		}
	}
	return template.HTML(fmt.Sprintf(`<svg viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Pipeline: %s">%s</svg>`,
		w, h, w, h, esc(strings.Join(names, ", ")), g.String()))
}

// chainSVG draws the rules of one file as boxes joined by their next
// links. A next that names no rule is drawn as a red dashed box.
func chainSVG(rules []wfRule) template.HTML {
	const bw, bh, gap, top = 120, 44, 100, 40
	w, h := len(rules)*(bw+gap)+150, 128
	idx := map[string]int{}
	for i, r := range rules {
		idx[r.Name] = i
		if r.Stage != "" {
			idx[r.Stage] = i
		}
	}
	var g strings.Builder
	var names []string
	for i, r := range rules {
		x := 10 + i*(bw+gap)
		names = append(names, r.Name)
		sub := r.Match
		if r.Stage != "" {
			sub = r.Stage
		}
		fmt.Fprintf(&g, `<rect class="box" x="%d" y="%d" width="%d" height="%d" rx="5"/><text x="%d" y="%d" text-anchor="middle" font-weight="700">%s</text><text class="m" x="%d" y="%d" text-anchor="middle">%s</text>`,
			x, top, bw, bh, x+bw/2, top+19, esc(clip(r.Name, 16)), x+bw/2, top+34, esc(clip(sub, 20)))
		ax, ay := x+bw, top+bh/2
		j, known := idx[r.Next]
		switch {
		case r.Next == "":
			fmt.Fprintf(&g, `<path d="M%d %dH%d"/><circle cx="%d" cy="%d" r="5" fill="var(--mut)"/><text class="m" x="%d" y="%d">handoff</text>`, ax, ay, ax+22, ax+28, ay, ax+38, ay+4)
		case !known:
			fmt.Fprintf(&g, `<path d="M%d %dH%d" stroke="var(--err)" stroke-dasharray="3 3"/><rect class="box bad" x="%d" y="%d" width="%d" height="28" rx="4"/><text x="%d" y="%d" style="fill:var(--err)">%s ?</text>`,
				ax, ay, ax+24, ax+24, ay-14, gap-30, ax+28, ay+4, esc(clip(r.Next, 8)))
		case j == i+1:
			tx := 10 + j*(bw+gap)
			fmt.Fprintf(&g, `<path d="M%d %dH%d"/><path class="arrow" d="M%d %dL%d %dL%d %dz"/>`, ax, ay, tx-6, tx-7, ay-4, tx, ay, tx-7, ay+4)
		default:
			tx := 10 + j*(bw+gap) + bw/2
			fmt.Fprintf(&g, `<path d="M%d %dC%d %d %d %d %d %d"/><path class="arrow" d="M%d %dL%d %dL%d %dz"/>`,
				ax-bw/2, top, ax-bw/2, top-34, tx, top-34, tx, top-3, tx-4, top-8, tx, top-1, tx+4, top-8)
		}
	}
	return template.HTML(fmt.Sprintf(`<svg viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Dispatch chain: %s">%s</svg>`,
		w, h, w, h, esc(strings.Join(names, ", ")), g.String()))
}
