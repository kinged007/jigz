package main

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// "fix(merge-queue)!: subject" → type "fix", scopes "merge-queue".
	conventionalRe = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^)]+)\))?!?:\s`)
	codeBlockRe    = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe   = regexp.MustCompile("`[^`\n]*`")
	urlRe          = regexp.MustCompile(`https?://\S+`)
	htmlCommentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tokenRe        = regexp.MustCompile(`[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*`)
	issueRefRe     = regexp.MustCompile(`(?:^|[^\w/])#(\d+)\b`)
	noreplyRe      = regexp.MustCompile(`^(?:\d+\+)?([^@]+)@users\.noreply\.github\.com$`)
)

// conventional parses a Conventional Commits style title.
func conventional(title string) (typ string, scopes []string) {
	m := conventionalRe.FindStringSubmatch(title)
	if m == nil {
		return "", nil
	}
	for _, s := range strings.Split(m[2], ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			scopes = append(scopes, s)
		}
	}
	return strings.ToLower(m[1]), scopes
}

func issueRefs(s string) []int {
	var out []int
	for _, m := range issueRefRe.FindAllStringSubmatch(s, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a about above after again against all also am an and any are as at be because been
before being below between both but by can cannot could did do does doing done down during each else even every few for
from further get gets got had has have having he her here hers him his how however i if in into is it its itself just
like make makes many may me might more most much must my need needs no nor not now of off on once one only or other our
ours out over own per same she should since so some such than that the their them then there these they this those
feat chore refactor perf style revert never
through thus to too under until up upon us use used uses using very via was we well were what when where which while who
whom why will with within without would yet you your new now first second next last still already instead rather either
neither whether each both able across among another around back based come comes currently does doesn etc fine given
goes going keep kept less let lets look looks lot made mean means set sets show shows take takes thing things want way
ways work works yes run runs running added adds add adding fix fixes fixed fixing update updates updated make true false
null nil none todo see per should shall must because without within line lines file files part parts step steps item
items case cases point points time times value values number numbers name names end ends start starts check checks
issue issues pr prs pull request requests comment comments body title description summary details detail context
acceptance criteria scope note notes test tests testing tested change changes changed closes close closed resolves
refs ref related following follow below above current existing default today currently true false return returns
https http www com github org dev main md go js ts json yaml yml txt e g eg ie vs no one two three four five`) {
		m[w] = true
	}
	return m
}()

// tokens returns the content words of s with markdown noise removed.
func tokens(s string) []string {
	s = htmlCommentRe.ReplaceAllString(s, " ")
	s = codeBlockRe.ReplaceAllString(s, " ")
	s = inlineCodeRe.ReplaceAllString(s, " ")
	s = urlRe.ReplaceAllString(s, " ")
	var out []string
	for _, w := range tokenRe.FindAllString(strings.ToLower(s), -1) {
		w = strings.Trim(w, "-_")
		if len(w) < 3 || len(w) > 30 || stopwords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// Keyword is a term with the number of documents (titles for keywords) using it.
type Keyword struct {
	Term string `json:"term"`
	DF   int    `json:"df"`
}

// keywordIndex runs TF-IDF over the documents (title weighted 3x) and returns
// the vocabulary and the top terms per document. The vocabulary is ranked by
// how many titles use a term, because bodies of agent-written issues share a
// lot of boilerplate. Terms in more than maxShare of documents are dropped.
func keywordIndex(titles, bodies []string, minDF int, maxShare float64, vocabSize, perDoc int) ([]Keyword, [][]string) {
	n := len(titles)
	tfs := make([]map[string]float64, n)
	df, titleDF := map[string]int{}, map[string]int{}
	for i := range titles {
		tf := map[string]float64{}
		for _, w := range tokens(titles[i]) {
			if tf[w] == 0 {
				titleDF[w]++
			}
			tf[w] += 3
		}
		for _, w := range tokens(bodies[i]) {
			tf[w]++
		}
		for w := range tf {
			df[w]++
		}
		tfs[i] = tf
	}
	maxDF := int(maxShare * float64(n))
	var vocab []Keyword
	for w, d := range titleDF {
		if d >= minDF && df[w] <= maxDF {
			vocab = append(vocab, Keyword{w, d})
		}
	}
	sort.Slice(vocab, func(i, j int) bool {
		if vocab[i].DF != vocab[j].DF {
			return vocab[i].DF > vocab[j].DF
		}
		return vocab[i].Term < vocab[j].Term
	})
	if len(vocab) > vocabSize {
		vocab = vocab[:vocabSize]
	}
	in := map[string]bool{}
	for _, k := range vocab {
		in[k.Term] = true
	}
	top := make([][]string, n)
	for i, tf := range tfs {
		type sc struct {
			w string
			s float64
		}
		var scores []sc
		for w, f := range tf {
			if in[w] {
				scores = append(scores, sc{w, (1 + math.Log(f)) * math.Log(float64(n)/float64(df[w]))})
			}
		}
		sort.Slice(scores, func(a, b int) bool {
			if scores[a].s != scores[b].s {
				return scores[a].s > scores[b].s
			}
			return scores[a].w < scores[b].w
		})
		for j := 0; j < len(scores) && j < perDoc; j++ {
			top[i] = append(top[i], scores[j].w)
		}
	}
	return vocab, top
}
