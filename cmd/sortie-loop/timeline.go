package main

import (
	"fmt"
	"html"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/store"
)

const (
	tlW, tlL, tlR = 880, 80, 180
	tlRow         = 26
	tlMinBar      = 3
	tlTicks       = 4
)

// attemptTimeline draws one bar per attempt on a shared time axis, with a
// connector for each retry gap and a row of the events central observed.
// The attempts need parsed times; without them it draws nothing.
func attemptTimeline(label string, rows []attemptRow, observed []store.Event) template.HTML {
	if len(rows) == 0 || rows[0].started.IsZero() {
		return ""
	}
	t0, t1 := rows[0].started, rows[0].started
	for _, r := range rows {
		t1 = laterOf(t1, laterOf(r.started, r.ended))
	}
	span := max(t1.Sub(t0), time.Second)
	plot := float64(tlW - tlL - tlR)
	x := func(t time.Time) float64 {
		return float64(tlL) + min(max(float64(t.Sub(t0))/float64(span), 0), 1)*plot
	}
	bottom := 26 + len(rows)*tlRow
	h := bottom + 20
	if len(observed) > 0 {
		h += 44
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="tl" viewBox="0 0 %d %d" width="100%%" role="img" aria-label="%s">`, tlW, h, html.EscapeString(label))
	for i := 0; i <= tlTicks; i++ {
		tx := float64(tlL) + plot*float64(i)/tlTicks
		fmt.Fprintf(&b, `<line class="axis" x1="%s" y1="20" x2="%s" y2="%d"/><text class="m" x="%s" y="12" text-anchor="middle">+%s</text>`,
			f1(tx), f1(tx), bottom, f1(tx), spanText(span*time.Duration(i)/tlTicks))
	}
	for i, r := range rows {
		y := 26 + i*tlRow
		end := laterOf(r.ended, r.started)
		xs, xe := x(r.started), x(end)
		fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="end">attempt %d</text>`, tlL-8, y+14, r.Attempt)
		if i+1 < len(rows) && !rows[i+1].started.IsZero() {
			ns := x(rows[i+1].started)
			gap := rows[i+1].Gap
			fmt.Fprintf(&b, `<path class="gap" d="M%s %d H%s V%d"><title>retry gap %s</title></path>`, f1(xe), y+11, f1(ns), y+tlRow+11, html.EscapeString(gap))
			if ns-xe >= 40 && gap != "" {
				fmt.Fprintf(&b, `<text class="m" x="%s" y="%d">%s</text>`, f1(xe+4), y+8, html.EscapeString(gap))
			}
		}
		title := "attempt " + strconv.Itoa(r.Attempt) + ": " + r.Status
		if r.Duration != "" {
			title += " after " + r.Duration
		}
		if r.Class.Label != "" {
			title += ", " + r.Class.Label
		}
		if r.Error != "" {
			title += ": " + r.Error
		}
		cls := "other"
		switch r.Status {
		case "succeeded":
			cls = "ok"
		case "failed":
			cls = "failed"
		case "cancelled":
			cls = "cancelled"
		}
		cur := ""
		if r.Current {
			cur = " cur"
		}
		fmt.Fprintf(&b, `<a href="%s"><rect class="seg-%s%s" x="%s" y="%d" width="%s" height="18" rx="2"><title>%s</title></rect></a>`,
			html.EscapeString(r.Href), cls, cur, f1(xs), y+2, f1(max(xe-xs, tlMinBar)), html.EscapeString(title))
		fmt.Fprintf(&b, `<text class="m" x="%d" y="%d">%s %s %s</text>`, tlW-tlR+10, y+15, clock(r.started), html.EscapeString(r.Duration), r.Status)
	}
	if len(observed) > 0 {
		ey := bottom + 8
		fmt.Fprintf(&b, `<text class="m" x="%d" y="%d" text-anchor="end">observed</text>`, tlL-8, ey+14)
		for _, e := range observed {
			t, ok := parseTS(e.TS)
			if !ok {
				continue
			}
			ex := x(t)
			fmt.Fprintf(&b, `<path class="ev" d="M%s %dl5 5-5 5-5-5z"><title>%s: %s (observed by central)</title></path>`,
				f1(ex), ey+3, clock(t), html.EscapeString(strings.TrimSpace(e.Kind+" "+e.Detail)))
		}
		fmt.Fprintf(&b, `<text class="m" x="%d" y="%d">Diamonds are events observed by central, not reported by the engine.</text>`, tlL, ey+34)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("15:04:05")
}

// timelineLabel is the accessible name of the timeline.
func timelineLabel(ident string, rows []attemptRow) string {
	failed := 0
	for _, r := range rows {
		if r.Status != "succeeded" {
			failed++
		}
	}
	s := "Attempt timeline for " + ident + ": " + strconv.Itoa(len(rows)) + " " + plural(len(rows), "attempt", "attempts")
	if failed > 0 {
		s += ", " + strconv.Itoa(failed) + " not succeeded"
	}
	return s
}
