package main

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strconv"
	"strings"
)

// Charts are drawn on the server as inline SVG. The page script only shows
// tooltips and hides series; it never draws.

const (
	chartW, chartH       = 880, 210
	chartL, chartR       = 50, 12
	chartT, chartB       = 10, 26
	seriesColors         = 6
	maxXLabels           = 10
	barLabelRunes        = 18
	sparkW, sparkH       = 100, 28
	barChartH            = 190
	barChartL, barChartB = 50, 34
)

// chartSeries is one line of a chart.
type chartSeries struct {
	Name, Color string
	Vals        []int64
}

// seriesColor is the colour of the i-th named series. Colours repeat after
// six.
func seriesColor(i int) string { return "var(--s" + strconv.Itoa(i%seriesColors+1) + ")" }

// compact shortens a count for an axis: 1200 is 1.2k, 3400000 is 3.4M.
func compact(v float64) string {
	trim := func(f float64, unit string) string {
		return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0") + unit
	}
	switch {
	case v >= 1e9:
		return trim(v/1e9, "G")
	case v >= 1e6:
		return trim(v/1e6, "M")
	case v >= 1e3:
		return trim(v/1e3, "k")
	}
	return strconv.FormatFloat(math.Round(v), 'f', 0, 64)
}

// niceMax rounds a maximum up to one significant figure so the axis labels
// are round numbers.
func niceMax(mx int64) float64 {
	if mx <= 0 {
		return 1
	}
	pw := math.Pow(10, math.Floor(math.Log10(float64(mx))))
	return math.Ceil(float64(mx)/pw) * pw
}

func maxOf(vals ...[]int64) int64 {
	var m int64
	for _, vs := range vals {
		for _, v := range vs {
			m = max(m, v)
		}
	}
	return m
}

func joinInts(vals []int64) string {
	s := make([]string, len(vals))
	for i, v := range vals {
		s[i] = strconv.FormatInt(v, 10)
	}
	return strings.Join(s, ",")
}

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// shortDay turns 2026-10-13 into 10-13.
func shortDay(d string) string {
	if len(d) == 10 {
		return d[5:]
	}
	return d
}

// lineChart draws one line per series over days. A legend, when asked for,
// has a button per series that hides it. The y axis is scaled to all series
// so hiding one does not rescale the rest.
func lineChart(days []string, series []chartSeries, area, legend bool, label string) template.HTML {
	n := len(days)
	nice := niceMax(maxOf(valsOf(series)...))
	x := func(i int) float64 {
		if n < 2 {
			return chartL + float64(chartW-chartL-chartR)/2
		}
		return chartL + float64(i)/float64(n-1)*float64(chartW-chartL-chartR)
	}
	y := func(v float64) float64 {
		return chartT + float64(chartH-chartT-chartB)*(1-v/nice)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="chartwrap" data-days="%s" data-l="%d" data-r="%d" data-w="%d">`,
		html.EscapeString(strings.Join(days, ",")), chartL, chartR, chartW)
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" width="100%%" role="img" aria-label="%s">`, chartW, chartH, html.EscapeString(label))
	for i := 0; i <= 4; i++ {
		v := nice * float64(i) / 4
		fmt.Fprintf(&b, `<line class="axis" x1="%d" x2="%d" y1="%s" y2="%s"/><text x="%d" y="%s" text-anchor="end">%s</text>`,
			chartL, chartW-chartR, f1(y(v)), f1(y(v)), chartL-6, f1(y(v)+3), compact(v))
	}
	step := (n + maxXLabels - 1) / maxXLabels
	for i, d := range days {
		if i%max(step, 1) == 0 {
			fmt.Fprintf(&b, `<text x="%s" y="%d" text-anchor="middle">%s</text>`, f1(x(i)), chartH-8, shortDay(d))
		}
	}
	for _, s := range series {
		pts := make([]string, len(s.Vals))
		for i, v := range s.Vals {
			pts[i] = f1(x(i)) + "," + f1(y(float64(v)))
		}
		fmt.Fprintf(&b, `<g data-series="%s" data-color="%s" data-vals="%s">`, html.EscapeString(s.Name), s.Color, joinInts(s.Vals))
		if area && n > 0 {
			fmt.Fprintf(&b, `<polygon points="%s,%s %s %s,%s" fill="%s" fill-opacity=".14"/>`,
				f1(x(0)), f1(y(0)), strings.Join(pts, " "), f1(x(n-1)), f1(y(0)), s.Color)
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round"/></g>`, strings.Join(pts, " "), s.Color)
	}
	fmt.Fprintf(&b, `<line class="cursor" x1="0" x2="0" y1="%d" y2="%d" stroke="var(--mut)" visibility="hidden"/>`, chartT, chartH-chartB)
	fmt.Fprintf(&b, `<rect class="hit" x="%d" y="0" width="%d" height="%d" fill="transparent"/></svg><div class="tip" hidden></div></div>`,
		chartL, chartW-chartL-chartR, chartH)
	if legend {
		b.WriteString(`<div class="legend">`)
		for _, s := range series {
			fmt.Fprintf(&b, `<button type="button" class="lg" data-legend="%s" aria-pressed="true"><i style="background:%s"></i>%s</button>`,
				html.EscapeString(s.Name), s.Color, html.EscapeString(s.Name))
		}
		b.WriteString(`</div>`)
	}
	return template.HTML(b.String())
}

func valsOf(series []chartSeries) [][]int64 {
	out := make([][]int64, len(series))
	for i, s := range series {
		out[i] = s.Vals
	}
	return out
}

// sparkline draws a small unlabelled trend.
func sparkline(vals []int64, color, label string) template.HTML {
	n := len(vals)
	mx := float64(max(maxOf(vals), 1))
	pts := make([]string, n)
	for i, v := range vals {
		px := float64(sparkW) / 2
		if n > 1 {
			px = float64(i) / float64(n-1) * sparkW
		}
		pts[i] = f1(px) + "," + f1(sparkH-2-(float64(v)/mx)*(sparkH-4))
	}
	line := strings.Join(pts, " ")
	return template.HTML(fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="%s"><polyline points="0,%d %s %d,%d" fill="%s" fill-opacity=".12" stroke="none"/><polyline points="%s" fill="none" stroke="%s" stroke-width="1.5" vector-effect="non-scaling-stroke"/></svg>`,
		sparkW, sparkH, html.EscapeString(label), sparkH, line, sparkW, sparkH, color, line, color))
}

// barSegment is one named part of a stacked bar.
type barSegment struct {
	Name, Color string
}

// stackedBars draws one bar per label, each stacked from stacks[i], one
// value per segment. A legend names the segments.
func stackedBars(labels []string, segs []barSegment, stacks [][]int64, label string) template.HTML {
	n := len(labels)
	var mx int64
	for _, st := range stacks {
		var sum int64
		for _, v := range st {
			sum += v
		}
		mx = max(mx, sum)
	}
	nice := niceMax(mx)
	plotH := float64(barChartH - chartT - barChartB)
	bw := float64(chartW-barChartL-chartR) / float64(max(n, 1))
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" width="100%%" role="img" aria-label="%s">`, chartW, barChartH, html.EscapeString(label))
	for i := 0; i <= 2; i++ {
		v := nice * float64(i) / 2
		yy := chartT + plotH*(1-v/nice)
		fmt.Fprintf(&b, `<line class="axis" x1="%d" x2="%d" y1="%s" y2="%s"/><text x="%d" y="%s" text-anchor="end">%s</text>`,
			barChartL, chartW-chartR, f1(yy), f1(yy), barChartL-6, f1(yy+3), compact(v))
	}
	step := max((n+maxXLabels-1)/maxXLabels, 1)
	for i, l := range labels {
		x := barChartL + float64(i)*bw + bw*.15
		w := bw * .7
		yy := chartT + plotH
		for j, v := range stacks[i] {
			if v <= 0 {
				continue
			}
			h := plotH * float64(v) / nice
			yy -= h
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"><title>%s: %s %s</title></rect>`,
				f1(x), f1(yy), f1(w), f1(h), segs[j].Color, html.EscapeString(l), commas(v), html.EscapeString(segs[j].Name))
		}
		if i%step == 0 {
			fmt.Fprintf(&b, `<text x="%s" y="%d" text-anchor="middle">%s</text>`, f1(x+w/2), barChartH-barChartB+14, html.EscapeString(trunc(l, barLabelRunes)))
		}
	}
	b.WriteString(`</svg>`)
	writeBarLegend(&b, segs)
	return template.HTML(b.String())
}

func writeBarLegend(b *strings.Builder, segs []barSegment) {
	b.WriteString(`<div class="legend">`)
	for _, s := range segs {
		fmt.Fprintf(b, `<span><i style="background:%s"></i>%s</span>`, s.Color, html.EscapeString(s.Name))
	}
	b.WriteString(`</div>`)
}

const (
	hBarRowH      = 26
	hBarMinW      = 640
	hBarLabelPx   = 6.2
	hBarValueRoom = 70
)

// horizontalBars draws one bar per label, each stacked from stacks[i], with
// the full label to the left and the total at the bar end. The label column
// is as wide as the longest label.
func horizontalBars(labels []string, segs []barSegment, stacks [][]int64, label string) template.HTML {
	n := len(labels)
	var mx int64
	longest := 0
	totals := make([]int64, n)
	for i, st := range stacks {
		for _, v := range st {
			totals[i] += v
		}
		mx = max(mx, totals[i])
		longest = max(longest, len([]rune(labels[i])))
	}
	nice := niceMax(mx)
	left := min(max(float64(longest)*hBarLabelPx+16, 90), 420)
	plotW := chartW - left - hBarValueRoom
	h := n*hBarRowH + 24
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="tw"><svg class="chart" viewBox="0 0 %d %d" width="100%%" style="min-width:%dpx" role="img" aria-label="%s">`, chartW, h, hBarMinW, html.EscapeString(label))
	for i := 0; i <= 4; i++ {
		x := left + plotW*float64(i)/4
		fmt.Fprintf(&b, `<line class="axis" x1="%s" x2="%s" y1="0" y2="%d"/><text x="%s" y="%d" text-anchor="middle">%s</text>`,
			f1(x), f1(x), h-18, f1(x), h-5, compact(nice*float64(i)/4))
	}
	for i, l := range labels {
		y := i*hBarRowH + 4
		fmt.Fprintf(&b, `<text x="%s" y="%d" text-anchor="end">%s</text>`, f1(left-8), y+13, html.EscapeString(l))
		x := left
		for j, v := range stacks[i] {
			if v <= 0 {
				continue
			}
			w := plotW * float64(v) / nice
			fmt.Fprintf(&b, `<rect x="%s" y="%d" width="%s" height="18" fill="%s"><title>%s: %s %s</title></rect>`,
				f1(x), y, f1(w), segs[j].Color, html.EscapeString(l), commas(v), html.EscapeString(segs[j].Name))
			x += w
		}
		fmt.Fprintf(&b, `<text x="%s" y="%d">%s</text>`, f1(x+5), y+13, compact(float64(totals[i])))
	}
	b.WriteString(`</svg></div>`)
	writeBarLegend(&b, segs)
	return template.HTML(b.String())
}
