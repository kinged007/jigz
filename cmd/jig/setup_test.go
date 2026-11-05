package main

import (
	"os"
	"testing"
)

func TestLabelOn(t *testing.T) {
	const exact = `[{"name":"type:bug","color":"d73a4a","description":"Bug"}]`
	const substring = `[{"name":"type:bugfix","color":"ffffff","description":"other"}]`
	cases := []struct {
		name   string
		stdout string
		want   *existingLabel
	}{
		{"empty output", "", nil},
		{"whitespace output", "\n", nil},
		{"empty array", "[]", nil},
		{"substring match only", substring, nil},
		{"exact match", exact, &existingLabel{color: "d73a4a", description: "Bug"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubCommand(t, "gh", "printf '%s' '"+tc.stdout+"'\n")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			got, err := labelOn("owner/repo", "type:bug")
			if err != nil {
				t.Fatalf("labelOn: %v", err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("labelOn = %+v, want %+v", got, tc.want)
			}
		})
	}
}
