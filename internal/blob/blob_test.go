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
