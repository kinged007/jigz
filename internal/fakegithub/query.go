package fakegithub

import (
	"fmt"
	"strings"
)

// term is one qualifier of a search query: key:v1,v2 or -key:v1.
// Several values in one term are an OR; separate terms are an AND.
type term struct {
	neg    bool
	key    string
	values []string
}

// Query is a parsed /search/issues "q" parameter.
type Query struct {
	terms []term
}

// supportedKeys are the qualifiers the engine and the shipped workflows
// send. Anything else is rejected rather than ignored, so a new qualifier
// in a workflow cannot pass the harness unevaluated.
var supportedKeys = map[string]bool{
	"repo": true, "type": true, "state": true,
	"label": true, "assignee": true, "milestone": true,
}

// ParseQuery splits q into qualifier terms. A value may be double-quoted
// to hold spaces, and a comma outside quotes separates alternatives. Only
// the first colon of a term ends the key, so label:agent:plan has the key
// "label" and the value "agent:plan". A bare word or an unknown key is an
// error.
func ParseQuery(q string) (Query, error) {
	var out Query
	for _, raw := range splitTerms(q) {
		t, err := parseTerm(raw)
		if err != nil {
			return Query{}, err
		}
		out.terms = append(out.terms, t)
	}
	return out, nil
}

// splitTerms breaks q on whitespace outside double quotes.
func splitTerms(q string) []string {
	var terms []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			terms = append(terms, cur.String())
			cur.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return terms
}

func parseTerm(raw string) (term, error) {
	var t term
	if strings.HasPrefix(raw, "-") {
		t.neg = true
		raw = raw[1:]
	}
	i := strings.Index(raw, ":")
	if i <= 0 {
		return t, fmt.Errorf("unsupported search term %q: expected key:value", raw)
	}
	t.key = strings.ToLower(raw[:i])
	if !supportedKeys[t.key] {
		return t, fmt.Errorf("unsupported search qualifier %q", t.key)
	}
	var cur strings.Builder
	inQuote := false
	for _, r := range raw[i+1:] {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			t.values = append(t.values, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return t, fmt.Errorf("unterminated quote in search term %q", raw)
	}
	t.values = append(t.values, cur.String())
	for _, v := range t.values {
		if err := checkValue(t.key, v); err != nil {
			return t, err
		}
	}
	return t, nil
}

func checkValue(key, v string) error {
	ok := true
	switch key {
	case "type":
		ok = v == "issue" || v == "pr"
	case "state":
		ok = v == "open" || v == "closed"
	}
	if !ok {
		return fmt.Errorf("unsupported value %q for search qualifier %q", v, key)
	}
	return nil
}

// Match reports whether it satisfies every term. me is the login that
// assignee:@me stands for.
func (q Query) Match(it *Item, me string) bool {
	for _, t := range q.terms {
		if t.holds(it, me) == t.neg {
			return false
		}
	}
	return true
}

// holds reports whether the item satisfies the term's positive form.
func (t term) holds(it *Item, me string) bool {
	for _, v := range t.values {
		if t.matchOne(it, me, v) {
			return true
		}
	}
	return false
}

func (t term) matchOne(it *Item, me, v string) bool {
	switch t.key {
	case "repo":
		return strings.EqualFold(it.Repo, v)
	case "type":
		return (v == "pr") == it.PR
	case "state":
		return it.State == v
	case "label":
		return containsFold(it.Labels, v)
	case "assignee":
		if v == "@me" {
			v = me
		}
		return containsFold(it.Assignees, v)
	case "milestone":
		return it.Milestone == v
	}
	return false
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
