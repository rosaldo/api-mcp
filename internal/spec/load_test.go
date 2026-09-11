package spec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Detection looks at CONTENT, never at the extension — because extensions lie: a spec served
// from a URL with no extension at all, a `.txt` holding OpenAPI, a `.json` that is really YAML.
func TestDetectsDialectFromContent(t *testing.T) {
	cases := []struct {
		name    string
		file    string // deliberately MISLEADING extension
		content string
		want    Kind
	}{
		{"OpenAPI 3 as JSON", "spec.yaml", `{"openapi":"3.0.0","paths":{}}`, KindOpenAPI},
		// Google's format says what it is in a field made for saying it — and it has to be
		// checked, because the rest of the document is ordinary JSON with none of the markers
		// the other dialects announce themselves by.
		{"Google discovery", "spec.yaml", `{"kind":"discovery#restDescription","name":"drive","resources":{}}`, KindDiscovery},
		{"OpenAPI 3.1 as YAML", "spec.json", "openapi: \"3.1.0\"\npaths: {}\n", KindOpenAPI},
		{"Swagger 2.0", "spec.txt", `{"swagger":"2.0","paths":{}}`, KindOpenAPI},
		{"GraphQL SDL", "schema.json", "type Query {\n  offers: [Offer!]!\n}\n", KindGraphQL},
		{"GraphQL introspection", "response.yaml", `{"data":{"__schema":{"types":[]}}}`, KindGraphQL},
		{"introspection without envelope", "s.json", `{"__schema":{"types":[]}}`, KindGraphQL},
		// A key repeated inside the same object. JSON allows it and keeps the last one; YAML
		// rejects the document. Published specs do it, and reading JSON as YAML made one
		// repeated key enough to lose the whole file, reported as if the format were unknown.
		{"JSON with a duplicate key", "dup.json",
			`{"swagger":"2.0","definitions":{"A":{"type":"object"},"A":{"type":"string"}},"paths":{}}`,
			KindOpenAPI},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), c.file)
			if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			doc, err := Load(context.Background(), p, "")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if doc.Kind != c.want {
				t.Errorf("Kind = %q, want %q", doc.Kind, c.want)
			}
		})
	}
}

// An unrecognised spec must not become a silent, empty server: whoever pointed at the wrong
// file needs to know at startup, not to find out later that the model has no tools to call.
func TestUnrecognisedSpecFailsLoudly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anything.json")
	if err := os.WriteFile(p, []byte(`{"title":"this is not a spec of anything"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), p, ""); err == nil {
		t.Error("unrecognised spec passed as valid")
	}
}

// `forced` exists for when the heuristic gets it wrong — so it has to BEAT detection, otherwise
// it is worth nothing.
func TestForcedKindBeatsDetection(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(`{"openapi":"3.0.0","paths":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := Load(context.Background(), p, KindGraphQL)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind != KindGraphQL {
		t.Errorf("Kind = %q, the forced value did not win", doc.Kind)
	}
}

// The title and description travel the same road as detection, and used to break on the same
// stone: a duplicate key made them come back empty, so the server started with no name and no
// instructions while every tool worked. Silent, and hard to trace back to one repeated line.
func TestTitleSurvivesADuplicateKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dup.json")
	body := `{"swagger":"2.0","info":{"title":"Some API","description":"what it does"},` +
		`"definitions":{"A":{"type":"object"},"A":{"type":"string"}},"paths":{}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := Load(context.Background(), p, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	title, description := doc.info()
	if title != "Some API" || description != "what it does" {
		t.Errorf("info() = %q / %q, want the values from the document", title, description)
	}
}

// The bound on downloading a spec is not arbitrary: multi-megabyte documents are ordinary, and
// one such URL measured 3s, 15s, 34s and 43s on four consecutive tries. Anything near 30s turns
// that spread into a server that starts on some days and not others.
func TestFetchTimeoutLeavesRoomForALargeSpec(t *testing.T) {
	if fetchTimeout < time.Minute {
		t.Errorf("fetchTimeout is %v: a spec that takes 43s to arrive would be cut off, and the "+
			"failure looks like a broken connector rather than a slow download", fetchTimeout)
	}
}

// TestDocumentedValuesMatchTheCode: the flag table already keeps `--flags` honest, and that left
// a gap wide enough to walk through — a change with no flag in it. The fetch bound moved from 30
// seconds to two minutes and the documentation, which never mentioned either, stayed just as
// true as before: silent, and only a reader would ever find out.
//
// So the numbers get the same treatment as the flags. One side reads the CONSTANT, the other
// reads the prose, and neither moves alone. A value with no wording here fails on purpose —
// choosing the words is how the documentation gets visited at all.
func TestDocumentedValuesMatchTheCode(t *testing.T) {
	inWords := map[time.Duration]string{
		30 * time.Second: "30 seconds",
		time.Minute:      "one minute",
		2 * time.Minute:  "two minutes",
		5 * time.Minute:  "five minutes",
	}
	words, ok := inWords[fetchTimeout]
	if !ok {
		t.Fatalf("fetchTimeout is now %v and this test has no wording for it: say it in the "+
			"README and in docs/architecture.md, then add the phrase here", fetchTimeout)
	}
	for _, doc := range []string{"../../README.md", "../../docs/architecture.md"} {
		b, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		if !strings.Contains(string(b), words) {
			t.Errorf("%s does not state the fetch bound as %q — it is %v in the code, and a "+
				"document that quietly describes the previous value is worse than one that "+
				"never mentioned it", doc, words, fetchTimeout)
		}
	}
}

// A specification can sit behind a key of its own. These cover the fetch, not the API calls.

// TestSpecHeadersReachTheServer: without them, an API that keeps its spec closed cannot be
// served at all — the fetch returns 401 and there are no tools to publish.
func TestSpecHeadersReachTheServer(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer spec-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{}}`))
	}))
	defer srv.Close()

	doc, err := LoadWithHeaders(context.Background(), srv.URL,
		"", map[string]string{"Authorization": "Bearer spec-secret"})
	if err != nil {
		t.Fatalf("fetching a spec behind a key: %v", err)
	}
	if doc == nil {
		t.Fatal("no document")
	}
	if h := got.Get("Authorization"); h != "Bearer spec-secret" {
		t.Errorf("Authorization reached the server as %q", h)
	}
}

// TestSpecFetchWithoutHeadersStillWorks: the headers are optional, and the old path — a public
// spec, no credential — must keep behaving exactly as before.
func TestSpecFetchWithoutHeadersStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Authorization")) != 0 {
			t.Errorf("an Authorization header was sent when none was asked for")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{}}`))
	}))
	defer srv.Close()

	if _, err := Load(context.Background(), srv.URL, ""); err != nil {
		t.Fatalf("fetching a public spec: %v", err)
	}
}

// TestSpecHeadersAreNotSentToLocalPaths is the canary for the leak that would be worst.
//
// A header holding a credential must not end up anywhere the operator did not name. A local
// path has no server to authenticate to, so passing headers there has to be a no-op rather than
// an error — and above all it must not make the read fail, which would turn an optional flag
// into a trap.
func TestSpecHeadersAreNotSentToLocalPaths(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(caminho,
		[]byte(`{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithHeaders(context.Background(), caminho, "",
		map[string]string{"Authorization": "Bearer spec-secret"}); err != nil {
		t.Fatalf("a local path with headers should just be read: %v", err)
	}
}
