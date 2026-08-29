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

// Inline is Offload's mirror: on the way IN, a file on disk becomes the base64 the API expects.
//
// The same asymmetry that made Offload necessary applies in reverse. To attach a 185 KB PDF to a
// Gmail message, the argument the model must produce is the whole RFC 2822 message, base64'd —
// a quarter of a megabyte it has to hold in its context and type out. The tool can take it; the
// model cannot carry it. Measured in the field: the send went through with a small attachment and
// was impossible with the real one, for that reason alone.
//
// So the model writes `file:<path>` and the bytes never pass through it. Only that prefix is
// touched, at any depth, in any string argument.
//
// SCOPED ON PURPOSE. Reading is confined to `dir` — without it, a spec (or a model reading a
// hostile page) could ask for any file the process can open. Everything is resolved and checked
// against the root, so `file:../../etc/shadow` does not leave it.
//
// Errors are NOT fatal, as in Offload: an argument that cannot be read goes through untouched and
// the API answers what it answers. Swallowing the call here would hide, behind our own error, a
// request the model would otherwise learn from.
func Inline(args map[string]any, dir string, urlSafe bool) map[string]any {
	if dir == "" || len(args) == 0 {
		return args
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return args
	}
	out, _ := inline(args, root, urlSafe)
	m, ok := out.(map[string]any)
	if !ok {
		return args
	}
	return m
}

// filePrefix is what marks an argument as a path rather than a value. Base64 has no colon, and
// prose that legitimately starts with `file:` is a URI — which is what this is.
const filePrefix = "file:"

func inline(node any, root string, urlSafe bool) (any, bool) {
	switch v := node.(type) {
	case map[string]any:
		changed := false
		for k, item := range v {
			novo, c := inline(item, root, urlSafe)
			if c {
				v[k] = novo
				changed = true
			}
		}
		return v, changed
	case []any:
		changed := false
		for i, item := range v {
			novo, c := inline(item, root, urlSafe)
			if c {
				v[i] = novo
				changed = true
			}
		}
		return v, changed
	case string:
		if !strings.HasPrefix(v, filePrefix) {
			return v, false
		}
		raw, err := readUnder(root, strings.TrimPrefix(v, filePrefix))
		if err != nil {
			return v, false
		}
		if urlSafe {
			return base64.URLEncoding.EncodeToString(raw), true
		}
		return base64.StdEncoding.EncodeToString(raw), true
	}
	return node, false
}

// readUnder reads a file only if it really sits under root. The check is on the RESOLVED path,
// because `..` and symlinks are exactly how a path that looks contained stops being so.
func readUnder(root, path string) ([]byte, error) {
	p := strings.TrimSpace(path)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("blob: %s is outside %s", path, root)
	}
	return os.ReadFile(real)
}
