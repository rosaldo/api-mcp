// Package spec brings a specification into the process and decides what it is.
//
// Two questions, in this order: WHERE it comes from (file, URL, stdin) and WHAT it is (OpenAPI
// 3, Swagger 2, GraphQL schema). No dialect answers the first, and no source answers the
// second — which is why both live here, away from whoever interprets the content.
package spec

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Kind is the specification's dialect.
type Kind string

const (
	KindOpenAPI Kind = "openapi" // OpenAPI 3.x or Swagger 2.0 (JSON or YAML)
	KindGraphQL Kind = "graphql" // GraphQL schema, as SDL or introspection
)

// Document is the loaded specification, still raw, already knowing what it is.
type Document struct {
	Source string // where it came from, so errors can point somewhere useful
	Raw    []byte
	Kind   Kind
}

// Load reads the spec from a file (`file://` or a plain path), an http(s) URL, or `-` (stdin).
// A non-empty `forced` beats detection — it exists for when the heuristic is wrong, so nobody
// is stuck with it.
func Load(ctx context.Context, source string, forced Kind) (*Document, error) {
	raw, err := read(ctx, source)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("empty spec at %s", source)
	}

	kind := forced
	if kind == "" {
		kind, err = detect(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
	}
	return &Document{Source: source, Raw: raw, Kind: kind}, nil
}

func read(ctx context.Context, source string) ([]byte, error) {
	switch {
	case source == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(source, "http://"), strings.HasPrefix(source, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching spec: %w", err)
		}
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching spec: %s returned %s", source, resp.Status)
		}
		// Capped so a URL pointing at something else cannot stream forever.
		return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	default:
		return os.ReadFile(strings.TrimPrefix(source, "file://"))
	}
}

// detect decides the dialect from the CONTENT, never from the file extension: `.json` and
// `.yaml` lie often (a spec served from a URL with no extension, a renamed file), while each
// format's markers are unambiguous.
//
// OpenAPI and Swagger announce themselves in a top-level field (`openapi:` / `swagger:`), and
// that is what we look for — after running the text through YAML, which also reads JSON (all
// JSON is valid YAML) and so covers both formats in a single pass.
func detect(raw []byte) (Kind, error) {
	var top map[string]any
	if err := yaml.Unmarshal(raw, &top); err == nil && top != nil {
		if _, ok := top["openapi"]; ok {
			return KindOpenAPI, nil
		}
		if _, ok := top["swagger"]; ok {
			return KindOpenAPI, nil
		}
		// GraphQL introspection result: `{"data": {"__schema": …}}` or `{"__schema": …}`.
		if hasIntrospectionSchema(top) {
			return KindGraphQL, nil
		}
	}
	// SDL is neither YAML nor JSON — it is GraphQL's own language. Recognise it by the
	// declarations every schema has: no `type`/`schema`/`extend`, no SDL.
	text := string(raw)
	for _, marker := range []string{"type Query", "type Mutation", "schema {", "schema{", "extend type"} {
		if strings.Contains(text, marker) {
			return KindGraphQL, nil
		}
	}
	return "", fmt.Errorf("unrecognised spec: neither OpenAPI/Swagger (no `openapi:` or `swagger:`) nor GraphQL (no SDL or introspection)")
}

func hasIntrospectionSchema(top map[string]any) bool {
	if _, ok := top["__schema"]; ok {
		return true
	}
	data, ok := top["data"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = data["__schema"]
	return ok
}
