package blob

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shape this exists for: the bytes in one field, their type in the field next to it. That is
// how Gemini answers an image, and how most APIs that inline media answer anything.
func payloadLike(data []byte, mime string) string {
	b, _ := json.Marshal(map[string]any{
		"status": "completed",
		"steps": []any{
			map[string]any{"content": []any{map[string]any{"text": "Here you go: ", "type": "text"}}},
			map[string]any{"content": []any{map[string]any{
				"data":      base64.StdEncoding.EncodeToString(data),
				"mime_type": mime,
				"type":      "image",
			}}},
		},
	})
	return string(b)
}

func TestOversizedBase64GoesToDiskAndTheModelGetsThePath(t *testing.T) {
	dir := t.TempDir()
	bytes := make([]byte, 40<<10)
	for i := range bytes {
		bytes[i] = byte(i % 251)
	}

	out := Offload(payloadLike(bytes, "image/png"), dir, MinBytes)

	if len(out) > 2000 {
		t.Errorf("the payload is still %d bytes — the point was to keep it out of the context", len(out))
	}
	if strings.Contains(out, base64.StdEncoding.EncodeToString(bytes[:64])) {
		t.Error("the base64 survived in the payload")
	}
	if !strings.Contains(out, "saved_to") || !strings.Contains(out, "image/png") {
		t.Errorf("the model was not told where the file is: %s", out)
	}
	// What did NOT change matters as much: everything around the bytes is still there.
	if !strings.Contains(out, "Here you go") || !strings.Contains(out, "completed") {
		t.Errorf("the rest of the answer was lost: %s", out)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) != 1 {
		t.Fatalf("expected one .png written, found %v", files)
	}
	written, _ := os.ReadFile(files[0])
	if len(written) != len(bytes) {
		t.Errorf("file has %d bytes, the original had %d", len(written), len(bytes))
	}
}

// Without a directory this package must be invisible: same string in, same string out. Anyone
// running api-mcp as before gets exactly what they got before.
func TestNoDirectoryMeansNoChange(t *testing.T) {
	in := payloadLike(make([]byte, 40<<10), "image/png")
	if out := Offload(in, "", MinBytes); out != in {
		t.Error("payload was rewritten with no blob dir set")
	}
}

func TestWhatMustNotBeTouched(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ name, in string }{
		// Prose is not base64 — spaces are not in the alphabet — but a long word-less string
		// could be, so the size floor is what keeps ordinary text safe.
		{"plain text answer", `{"text":"` + strings.Repeat("a sentence with spaces ", 500) + `"}`},
		{"small base64 stays inline", `{"data":"` + base64.StdEncoding.EncodeToString([]byte("tiny")) + `","mime_type":"image/png"}`},
		{"not JSON at all", "HTTP 500: the upstream fell over"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Offload(c.in, dir, MinBytes); out != c.in {
				t.Errorf("payload was rewritten:\n in: %.80s\nout: %.80s", c.in, out)
			}
		})
	}
}

// The same bytes twice are one file, not two: the name is the content's own digest.
func TestSameContentLandsOnOneFile(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 20<<10)
	for i := range data {
		data[i] = byte(i)
	}
	first := Offload(payloadLike(data, "audio/wav"), dir, MinBytes)
	second := Offload(payloadLike(data, "audio/wav"), dir, MinBytes)
	if first != second {
		t.Error("the same content produced two different paths")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.wav"))
	if len(files) != 1 {
		t.Errorf("expected one file, found %v", files)
	}
}

// And the other half of the same guarantee: two different files must not land on one path. A
// fixed name would pass the test above and silently overwrite one generation with the next.
func TestDifferentContentLandsOnDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	a := make([]byte, 20<<10)
	b := make([]byte, 20<<10)
	for i := range b {
		b[i] = 1
	}
	if Offload(payloadLike(a, "image/png"), dir, MinBytes) == Offload(payloadLike(b, "image/png"), dir, MinBytes) {
		t.Error("two different images were given the same path")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) != 2 {
		t.Errorf("expected two files, found %v — one generation overwrote the other", files)
	}
}

// Run against a real captured API response by pointing BLOB_REAL_PAYLOAD at it. Kept out of the
// repository on purpose: a fixture of a megabyte is a megabyte everyone clones forever.
func TestAgainstARealResponse(t *testing.T) {
	path := os.Getenv("BLOB_REAL_PAYLOAD")
	if path == "" {
		t.Skip("set BLOB_REAL_PAYLOAD to a captured response to run this")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := Offload(string(raw), dir, MinBytes)
	t.Logf("payload went from %d to %d bytes", len(raw), len(out))
	if len(out) >= len(raw)/10 {
		t.Errorf("expected the payload to collapse, got %d from %d", len(out), len(raw))
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		fi, _ := f.Info()
		t.Logf("wrote %s (%d bytes)", f.Name(), fi.Size())
	}
}

// TestAPathBecomesTheBytes: the mirror of Offload, and it exists for the same reason — the model
// cannot carry the payload. Attaching a 185 KB PDF to a Gmail message means producing the whole
// message, base64'd, AS AN ARGUMENT: a quarter of a megabyte typed out by the model. Measured in
// the field: the send went through with a small attachment and was impossible with the real one.
func TestAPathBecomesTheBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.pdf"), []byte("%PDF-1.7 content"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 content"))

	args := map[string]any{
		"subject": "report",
		"message": map[string]any{"raw": "file:a.pdf"},
		"list":    []any{"file:a.pdf"},
	}
	out := Inline(args, dir, false)

	if got := out["message"].(map[string]any)["raw"]; got != want {
		t.Errorf("nested = %v, want the bytes in base64", got)
	}
	if got := out["list"].([]any)[0]; got != want {
		t.Errorf("inside a list = %v, want the bytes in base64", got)
	}
	if got := out["subject"]; got != "report" {
		t.Errorf("something that is not `file:` was touched: %v", got)
	}
}

// TestReadingStaysUnderTheRoot: without this, a third-party spec — or a model that read a hostile
// page — asks for any file the process can open. The check is on the RESOLVED path, because `..`
// and symlinks are exactly how a path that looks contained stops being one.
func TestReadingStaysUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("must not leave"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "shortcut")); err != nil {
		t.Skipf("no symlinks on this machine: %v", err)
	}

	for _, path := range []string{
		"file:" + secret, // absolute, outside
		"file:../" + filepath.Base(outside) + "/secret", // climbing out with ..
		"file:shortcut", // a symlink pointing outside
	} {
		out := Inline(map[string]any{"x": path}, root, false)
		if got := out["x"]; got != path {
			t.Errorf("%s was read — the argument should pass through untouched, got %.20q", path, got)
		}
	}
}

// TestTheDialectPicksTheAlphabet: Google declares the `format: byte` fields of its APIs as
// base64URL (Gmail's own discovery document says "base64url encoded string" on `raw`), while
// OpenAPI's `format: byte` is plain base64. Sending one for the other produces an API error that
// mentions no alphabet at all.
func TestTheDialectPicksTheAlphabet(t *testing.T) {
	dir := t.TempDir()
	// These bytes produce `+` and `/` in plain base64, and `-` and `_` in base64URL.
	raw := []byte{0xfb, 0xff, 0xbf}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	plain := Inline(map[string]any{"x": "file:b.bin"}, dir, false)["x"].(string)
	urlSafe := Inline(map[string]any{"x": "file:b.bin"}, dir, true)["x"].(string)

	if !strings.ContainsAny(plain, "+/") {
		t.Errorf("plain base64 = %q, expected + or /", plain)
	}
	if strings.ContainsAny(urlSafe, "+/") {
		t.Errorf("base64URL = %q, must contain neither + nor /", urlSafe)
	}
	if plain == urlSafe {
		t.Error("both alphabets produced the same thing — the parameter is not being used")
	}
}
