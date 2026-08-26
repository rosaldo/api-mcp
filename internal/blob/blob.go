// Package blob keeps generated media out of the model's context.
//
// Some APIs answer with the file itself, base64'd into the JSON: a Gemini image comes back as
// 1.17 MB in `content[0].data`, a Lyria clip as 993 KB. Handing that to the model is roughly
// 300,000 tokens for one call, and the model cannot do anything with the bytes anyway — it
// cannot look at them, and it cannot save them.
//
// So the bytes go to a directory and the model gets the path instead. What it needs to know is
// where the file is, how big it is and what it is; that fits in one line.
package blob

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MinBytes is the size from which a base64 string is worth writing out. Below it, the file would
// cost more in path and ceremony than in tokens.
const MinBytes = 8 << 10

// Offload rewrites the payload, replacing every oversized base64 string with a small object
// pointing at the file it was written to. Anything that is not JSON, or has nothing big in it,
// comes back untouched — this is a pass-through by default, never a transformation the caller
// has to think about.
//
// Errors are deliberately NOT fatal: a response that could not be written to disk is still a
// response the model should see. In that case the original payload goes through unchanged, and
// the caller is left with the same behaviour it had before this package existed.
func Offload(payload, dir string, min int) string {
	if dir == "" {
		return payload
	}
	if min <= 0 {
		min = MinBytes
	}
	var doc any
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return payload // not JSON: nothing to walk
	}
	out, changed := walk(doc, dir, min, "")
	if !changed {
		return payload
	}
	b, err := json.Marshal(out)
	if err != nil {
		return payload
	}
	return string(b)
}

// walk descends the decoded JSON. `mime` carries the sibling mime type down one level, because
// that is how these payloads are shaped: `{"data": "<base64>", "mime_type": "image/png"}` — the
// type of the bytes is next to them, not inside them.
func walk(node any, dir string, min int, mime string) (any, bool) {
	switch v := node.(type) {
	case map[string]any:
		sibling := mimeOf(v)
		changed := false
		for k, item := range v {
			novo, c := walk(item, dir, min, sibling)
			if c {
				v[k] = novo
				changed = true
			}
		}
		return v, changed
	case []any:
		changed := false
		for i, item := range v {
			novo, c := walk(item, dir, min, mime)
			if c {
				v[i] = novo
				changed = true
			}
		}
		return v, changed
	case string:
		if len(v) < min {
			return v, false
		}
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return v, false // plain text: prose has spaces, and those are not base64
		}
		saved, err := write(raw, dir, mime)
		if err != nil {
			return v, false // could not write: the model still gets its answer
		}
		return saved, true
	}
	return node, false
}

// mimeOf finds the mime type declared next to the bytes, whatever the API chose to call the key.
func mimeOf(obj map[string]any) string {
	for k, v := range obj {
		lower := strings.ToLower(k)
		if lower == "mime_type" || lower == "mimetype" || lower == "content_type" {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// write puts the bytes on disk and describes them. The name is the content's own digest, so the
// same file generated twice occupies one path instead of two.
func write(raw []byte, dir, mime string) (map[string]any, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	name := fmt.Sprintf("%x%s", sum[:8], extensionFor(mime))
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, err
		}
	}
	saved := map[string]any{"saved_to": path, "bytes": len(raw)}
	if mime != "" {
		saved["mime_type"] = mime
	}
	return saved, nil
}

// extensionFor turns a mime type into a file extension. Unknown types get `.bin`: a wrong
// extension is worse than none, because it makes a player fail in a way nobody can read.
func extensionFor(mime string) string {
	mime, _, _ = strings.Cut(mime, ";")
	mime = strings.TrimSpace(strings.ToLower(mime))
	if known, ok := map[string]string{
		"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
		"audio/wav": ".wav", "audio/x-wav": ".wav", "audio/mpeg": ".mp3", "audio/mp3": ".mp3",
		"audio/ogg": ".ogg", "audio/flac": ".flac", "audio/aac": ".aac", "audio/l16": ".pcm",
		"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
		"application/pdf": ".pdf", "text/plain": ".txt", "application/json": ".json",
	}[mime]; ok {
		return known
	}
	if _, sub, ok := strings.Cut(mime, "/"); ok && sub != "" && isWordy(sub) {
		return "." + sub
	}
	return ".bin"
}

func isWordy(s string) bool {
	if len(s) > 12 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
