// Package openapi translates OpenAPI 3.x and Swagger 2.0 into callable operations.
//
// The translator does not write a parser: `kin-openapi` already resolves `$ref` (nested and
// external included), reads JSON and YAML, and understands 3.0 and 3.1; Swagger 2.0 comes in
// converted to 3. Writing that by hand costs a partial model — and whatever the model does not
// cover arrives impoverished at the other end.
//
// Here each parameter's schema is handed over AS IT IS in the spec (enum, format, array items,
// nested objects): the reader is an LLM, and every field lost along the way is a call it has to
// guess at.
package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/rosaldo/api-mcp/internal/auth"
	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/spec"
)

// Options are decisions made by whoever starts the server, not by the spec.
type Options struct {
	BaseURL        string // beats the spec's `servers`
	Auth           auth.Applier
	Client         *http.Client
	IncludePaths   []*regexp.Regexp
	ExcludePaths   []*regexp.Regexp
	IncludeMethods []string
	ExcludeMethods []string
	Headers        map[string]string // fixed headers on every call
}

// Operations translates the whole document.
func Operations(ctx context.Context, doc *spec.Document, o Options) ([]core.Operation, error) {
	t, err := load(ctx, doc)
	if err != nil {
		return nil, err
	}
	base := o.BaseURL
	if base == "" {
		base = baseFromSpec(t)
	}
	if base == "" {
		return nil, fmt.Errorf("the spec does not say where the API lives and no --base-url was given")
	}
	// `None` is normalised to nil on purpose: with no authentication configured, an
	// `Authorization` declared in the spec is exactly what the model needs to fill in.
	if _, isNone := o.Auth.(auth.None); isNone {
		o.Auth = nil
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}

	var ops []core.Operation
	used := map[string]int{}
	for _, path := range sortedPaths(t.Paths.Map()) {
		item := t.Paths.Value(path)
		if !pathAllowed(path, o.IncludePaths, o.ExcludePaths) {
			continue
		}
		for method, op := range item.Operations() {
			if !methodAllowed(method, o.IncludeMethods, o.ExcludeMethods) {
				continue
			}
			ops = append(ops, build(strings.TrimSuffix(base, "/"), path, method, op, item, o, used))
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("the spec yielded no operations (too many filters, or empty `paths`)")
	}
	return ops, nil
}

// load hands back a 3.x document, wherever it came from.
func load(ctx context.Context, doc *spec.Document) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.Context = ctx
	loader.IsExternalRefsAllowed = true

	// Swagger 2.0 is not read by the 3.x loader, so it gets converted first. The conversion is
	// kin-openapi's own, so the result is the same model as the rest of the path.
	if isSwagger2(doc.Raw) {
		var v2 openapi2.T
		if err := json.Unmarshal(toJSON(doc.Raw), &v2); err != nil {
			return nil, fmt.Errorf("reading Swagger 2.0: %w", err)
		}
		t, err := openapi2conv.ToV3(&v2)
		if err != nil {
			return nil, fmt.Errorf("converting Swagger 2.0 to OpenAPI 3: %w", err)
		}
		return t, nil
	}

	t, err := loader.LoadFromData(doc.Raw)
	if err != nil {
		return nil, fmt.Errorf("reading OpenAPI: %w", err)
	}
	return t, nil
}

func isSwagger2(raw []byte) bool {
	var top struct {
		Swagger string `json:"swagger"`
	}
	_ = json.Unmarshal(toJSON(raw), &top)
	return strings.HasPrefix(top.Swagger, "2.")
}

// toJSON accepts YAML and returns JSON; JSON passes through untouched. The openapi2 model only
// reads JSON, and the spec may well have arrived as YAML.
func toJSON(raw []byte) []byte {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return raw
	}
	var anything any
	if err := yamlUnmarshal(raw, &anything); err != nil {
		return raw
	}
	b, err := json.Marshal(anything)
	if err != nil {
		return raw
	}
	return b
}

func baseFromSpec(t *openapi3.T) string {
	if len(t.Servers) == 0 {
		return ""
	}
	return t.Servers[0].URL
}

func sortedPaths(m map[string]*openapi3.PathItem) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	// Stable order: the tool list must not shuffle between two starts of the server, or two
	// identical containers would announce different catalogues.
	sort.Strings(names)
	return names
}

func pathAllowed(path string, include, exclude []*regexp.Regexp) bool {
	for _, re := range exclude {
		if re.MatchString(path) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, re := range include {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

func methodAllowed(method string, include, exclude []string) bool {
	m := strings.ToUpper(method)
	for _, e := range exclude {
		if strings.EqualFold(e, m) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, i := range include {
		if strings.EqualFold(i, m) {
			return true
		}
	}
	return false
}
