package mcpserver

import "testing"

// TestTheFileNoticeOnlyExistsWhenItWorks: the prefix is a capability, and a capability the model
// is not told about does not exist — measured on 2026-08-29, when `--blob-in` was configured and
// running and the model still sent a bare path, because nothing announced it.
//
// The other half matters as much: with the flag off, saying nothing is the point. An instruction
// describing a prefix that would be passed through untouched is worse than silence — the model
// would use it and the API would receive the literal string `file:...`.
func TestTheFileNoticeOnlyExistsWhenItWorks(t *testing.T) {
	if got := blobInNotice(""); got != "" {
		t.Errorf("with the flag off the notice must be empty, got %q", got)
	}
	got := blobInNotice("/home/x/workspace")
	for _, want := range []string{"file:", "/home/x/workspace"} {
		if !contains(got, want) {
			t.Errorf("the notice does not mention %q: %s", want, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
