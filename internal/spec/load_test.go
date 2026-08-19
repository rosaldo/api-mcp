package spec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
		{"OpenAPI 3.1 as YAML", "spec.json", "openapi: \"3.1.0\"\npaths: {}\n", KindOpenAPI},
		{"Swagger 2.0", "spec.txt", `{"swagger":"2.0","paths":{}}`, KindOpenAPI},
		{"GraphQL SDL", "schema.json", "type Query {\n  offers: [Offer!]!\n}\n", KindGraphQL},
		{"GraphQL introspection", "response.yaml", `{"data":{"__schema":{"types":[]}}}`, KindGraphQL},
		{"introspection without envelope", "s.json", `{"__schema":{"types":[]}}`, KindGraphQL},
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
