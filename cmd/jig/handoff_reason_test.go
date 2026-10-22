package main

import "testing"

func TestHandoffReasonReadsStoredAndAPIErrorText(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"stored absence", "handoff withheld: absence of work observed under observed policy", "absence of work observed under observed policy"},
		{"stored undetermined", "handoff withheld: evidence not determinable", "evidence not determinable"},
		{"sentence with detail", "Sortie did not hand the issue off: no output.", "no output"},
		{"sentence for absence", "The agent finished without changing anything, so Sortie did not hand the issue off.", ""},
		{"sentence for undetermined", "Sortie could not tell whether the agent changed anything, so it did not hand the issue off.", ""},
		{"other error", "agent crashed", ""},
	} {
		if got := handoffReason(c.text); got != c.want {
			t.Errorf("%s: handoffReason(%q) = %q, want %q", c.name, c.text, got, c.want)
		}
	}
}
