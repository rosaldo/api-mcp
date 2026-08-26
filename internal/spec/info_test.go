package spec

import (
	"strings"
	"testing"
)

// Under tool search, a client loads tool NAMES and the server instructions at session start and
// defers everything else. A server with no instructions is a server the model has no reason to
// search — so these tests guard the text that decides whether the tools are found at all.
func TestInstructionsComeFromTheSpecItself(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			"title and description are joined",
			"info:\n  title: cobalt\n  description: Download video from a link.\npaths: {}\n",
			"cobalt — Download video from a link.",
		},
		{
			"title alone still says who this is",
			`{"info":{"title":"OKX v5"},"paths":{}}`,
			"OKX v5",
		},
		{
			// A LITERAL block (`|`) keeps every newline, and JSON descriptions carry "\n" just
			// as happily. The client renders instructions as one paragraph, so the whitespace is
			// flattened here rather than at every call site. (A folded block, `>-`, is already
			// one line by the time YAML is done with it — it would pass without any of this.)
			"a literal block becomes one line",
			"info:\n  title: X\n  description: |\n    first line\n    second   line\npaths: {}\n",
			"X — first line second line",
		},
		{
			// GraphQL SDL, a bare schema, anything without `info`: no instructions, and that is
			// a valid answer — the caller decides what to do with an empty string.
			"no info yields nothing",
			"type Query { a: String }",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := (&Document{Raw: []byte(c.raw)}).Instructions()
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Claude Code truncates instructions at 2KB. Text past that limit is written for nobody, and a
// sentence cut mid-word reads like a bug to whoever sees it.
func TestInstructionsStayUnderTheClientLimit(t *testing.T) {
	long := "info:\n  title: T\n  description: " + strings.Repeat("word ", 1000) + "\npaths: {}\n"
	got := (&Document{Raw: []byte(long)}).Instructions()

	if len(got) > InstructionsLimit {
		t.Errorf("instructions are %d bytes, over the %d limit", len(got), InstructionsLimit)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation should be visible, got tail %q", got[max(0, len(got)-20):])
	}
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), "wor") {
		t.Error("cut in the middle of a word")
	}
	if !strings.HasPrefix(got, "T — word") {
		t.Errorf("the start — the part that survives — was lost: %q", got[:20])
	}
}
