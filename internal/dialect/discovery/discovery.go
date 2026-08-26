// Package discovery reads Google's Discovery Document — the format they publish for their own
// REST APIs, and which no OpenAPI parser understands.
//
// It is worth a dialect of its own because it is not one API: Google serves a discovery document
// for over three hundred services — Drive, Sheets, Calendar, Gmail, YouTube, Search Console,
// Analytics — all at a predictable address, all in the same shape. Written once, every one of
// them becomes reachable without a line of code per API.
//
// What it is NOT good for: an API whose surface is huge. Drive has 64 methods, YouTube 83, Gmail
// 79 — publishing them whole gives a model dozens of tools it will never call, and a wrong option
// to pick among them. Use `--include-paths` to say what this connector is actually for.
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/rosaldo/api-mcp/internal/auth"
	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/spec"
)

// Options are decisions made by whoever starts the server, not by the document.
type Options struct {
	BaseURL        string // beats the document's own baseUrl
	Auth           auth.Applier
	Client         *http.Client
	IncludePaths   []*regexp.Regexp
	ExcludePaths   []*regexp.Regexp
	IncludeMethods []string
	ExcludeMethods []string
	Headers        map[string]string
	Depth          int // how far $ref chains are expanded; see DefaultDepth
}

type document struct {
	Kind        string                     `json:"kind"`
	Name        string                     `json:"name"`
	Version     string                     `json:"version"`
	BaseURL     string                     `json:"baseUrl"`
	RootURL     string                     `json:"rootUrl"`
	ServicePath string                     `json:"servicePath"`
	Schemas     map[string]json.RawMessage `json:"schemas"`
	Resources   map[string]resource        `json:"resources"`
}

type resource struct {
	Methods   map[string]method   `json:"methods"`
	Resources map[string]resource `json:"resources"`
}

type method struct {
	ID          string               `json:"id"`
	Path        string               `json:"path"`
	HTTPMethod  string               `json:"httpMethod"`
	Description string               `json:"description"`
	Parameters  map[string]parameter `json:"parameters"`
	Request     *struct {
		Ref string `json:"$ref"`
	} `json:"request"`
}

type parameter struct {
	Type             string   `json:"type"`
	Description      string   `json:"description"`
	Required         bool     `json:"required"`
	Location         string   `json:"location"` // "path" or "query"
	Enum             []string `json:"enum"`
	EnumDescriptions []string `json:"enumDescriptions"`
	Format           string   `json:"format"`
	Repeated         bool     `json:"repeated"`
	Default          string   `json:"default"`
}

// Operations translates the whole document.
func Operations(ctx context.Context, doc *spec.Document, o Options) ([]core.Operation, error) {
	var d document
	if err := json.Unmarshal(doc.Raw, &d); err != nil {
		return nil, fmt.Errorf("this is not a discovery document: %w", err)
	}
	base := o.BaseURL
	if base == "" {
		base = d.baseAddress()
	}
	if base == "" {
		return nil, fmt.Errorf("the document does not say where the API lives and no --base-url was given")
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}
	if o.Depth <= 0 {
		o.Depth = DefaultDepth
	}
	if _, isNone := o.Auth.(auth.None); isNone {
		o.Auth = nil
	}

	var ops []core.Operation
	walk(d.Resources, "", func(m method) {
		if !allowed(m, o) {
			return
		}
		ops = append(ops, build(d, m, base, o))
	})
	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	return ops, nil
}

// baseAddress: `baseUrl` is the whole address and is what most documents carry; `rootUrl` +
// `servicePath` is the older pair, still present in some. Either way the result ends in a slash,
// because every method's `path` is relative to it.
func (d document) baseAddress() string {
	if d.BaseURL != "" {
		return d.BaseURL
	}
	if d.RootURL != "" {
		return strings.TrimSuffix(d.RootURL, "/") + "/" + strings.TrimPrefix(d.ServicePath, "/")
	}
	return ""
}

// walk descends resources, which nest arbitrarily: `fileSearchStores.documents.list` is three
// levels down, and there is no limit declared anywhere.
func walk(resources map[string]resource, prefix string, visit func(method)) {
	for _, name := range sortedKeys(resources) {
		r := resources[name]
		for _, mname := range sortedKeys(r.Methods) {
			visit(r.Methods[mname])
		}
		walk(r.Resources, prefix+name+".", visit)
	}
}

func allowed(m method, o Options) bool {
	verb := strings.ToUpper(m.HTTPMethod)
	if len(o.IncludeMethods) > 0 && !slices.Contains(o.IncludeMethods, verb) {
		return false
	}
	if slices.Contains(o.ExcludeMethods, verb) {
		return false
	}
	// Filters read the PATH, the same thing `--include-paths` matches on in OpenAPI, so one flag
	// means one thing across dialects. A leading slash is added because that is how a person
	// writes the regex: `^/files`.
	path := "/" + strings.TrimPrefix(m.Path, "/")
	for _, re := range o.ExcludePaths {
		if re.MatchString(path) {
			return false
		}
	}
	if len(o.IncludePaths) == 0 {
		return true
	}
	for _, re := range o.IncludePaths {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// build turns one method into a tool.
func build(d document, m method, base string, o Options) core.Operation {
	in := core.NewObjectSchema()
	var required []string

	for _, name := range sortedKeys(m.Parameters) {
		p := m.Parameters[name]
		in.Properties[name] = p.schema()
		if p.Required {
			required = append(required, name)
		}
	}

	// The request body arrives as a `$ref` into the schema table. Its fields are merged into the
	// same flat argument object the parameters live in: a model filling one object is a model
	// making fewer mistakes than one deciding what belongs in a nested `body`.
	bodyFields := map[string]bool{}
	if m.Request != nil && m.Request.Ref != "" {
		if raw, ok := d.Schemas[m.Request.Ref]; ok {
			body := jsonSchema(d.Schemas, raw, o.Depth, map[string]bool{m.Request.Ref: true})
			if props, ok := body["properties"].(map[string]any); ok {
				for name, sub := range props {
					if _, clash := in.Properties[name]; clash {
						continue // a parameter of the same name wins: it is the one in the URL
					}
					in.Properties[name] = sub
					bodyFields[name] = true
				}
				if req, ok := body["required"].([]string); ok {
					required = append(required, req...)
				}
			}
		}
	}
	sort.Strings(required)
	in.Required = required

	return core.Operation{
		Name:        toolName(m.ID),
		Description: describe(m),
		Input:       in,
		Invoke: func(ctx context.Context, args map[string]any) (string, error) {
			return execute(ctx, base, m, bodyFields, args, o)
		},
	}
}

// toolName drops the service prefix from the method id: `drive.files.get` is `files_get` here,
// because the server already is Drive — repeating it in every tool name buys nothing.
func toolName(id string) string {
	parts := strings.Split(id, ".")
	if len(parts) > 1 {
		parts = parts[1:]
	}
	name := strings.Join(parts, "_")
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
}

func describe(m method) string {
	if d := strings.TrimSpace(m.Description); d != "" {
		return d
	}
	return strings.ToUpper(m.HTTPMethod) + " " + m.Path
}

func (p parameter) schema() map[string]any {
	out := map[string]any{}
	switch p.Type {
	case "integer", "number", "boolean", "string":
		out["type"] = p.Type
	default:
		out["type"] = "string"
	}
	if p.Repeated {
		out = map[string]any{"type": "array", "items": map[string]any{"type": out["type"]}}
	}
	desc := p.Description
	if len(p.Enum) > 0 {
		out["enum"] = p.Enum
		if d := describeEnum(p.Enum, p.EnumDescriptions); d != "" {
			desc = joinNonEmpty(desc, d)
		}
	}
	if p.Default != "" {
		out["default"] = p.Default
	}
	if p.Format != "" {
		out["format"] = p.Format
	}
	if desc != "" {
		out["description"] = desc
	}
	return out
}

// placeholder matches both templates discovery uses: `{name}`, and `{+name}` — the reserved form,
// where the value may hold slashes and must NOT be escaped. `models/x/operations/y` arrives as one
// value in a single `{+name}`, and percent-encoding its slashes turns a valid call into a 404.
var placeholder = regexp.MustCompile(`\{(\+?)([^}]+)\}`)

func execute(ctx context.Context, base string, m method, bodyFields map[string]bool, args map[string]any, o Options) (string, error) {
	path := m.Path
	query := url.Values{}
	body := map[string]any{}
	var missing []string

	path = placeholder.ReplaceAllStringFunc(path, func(match string) string {
		g := placeholder.FindStringSubmatch(match)
		reserved, name := g[1] == "+", g[2]
		value, ok := args[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		if reserved {
			return asText(value) // slashes are part of the value here
		}
		return url.PathEscape(asText(value))
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing path parameter %s", strings.Join(missing, ", "))
	}

	for name, value := range args {
		p, isParam := m.Parameters[name]
		switch {
		case isParam && p.Location == "path":
			// already in the URL
		case isParam:
			for _, v := range asList(value) {
				query.Add(name, v) // repeated parameters are repeated, not joined
			}
		case bodyFields[name]:
			body[name] = value
		default:
			body[name] = value // unknown to us: the API is the one that decides, not this parser
		}
	}

	target := base + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var payload io.Reader
	if len(body) > 0 {
		raw, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(m.HTTPMethod), target, payload)
	if err != nil {
		return "", err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	if o.Auth != nil {
		if err := o.Auth.Apply(ctx, req); err != nil {
			return "", err
		}
	}

	resp, err := o.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", target, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s %s returned %s: %s", strings.ToUpper(m.HTTPMethod), target, resp.Status, firstChars(string(data), 500))
	}
	return string(data), nil
}

func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return strings.Trim(string(raw), `"`)
	}
}

func asList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return []string{asText(v)}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asText(item))
	}
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
