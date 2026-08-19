package openapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// asJSONSchema hands the spec's schema to the model AS IT IS.
//
// kin-openapi's own serialisation already produces JSON Schema — with enum, format, array
// items, nested objects, minimum and maximum. Rebuilding that by hand throws away exactly the
// information that makes the model get the call right on the first try.
func asJSONSchema(s *openapi3.Schema) map[string]any {
	if s == nil {
		return map[string]any{"type": "string"}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return map[string]any{"type": "string"}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"type": "string"}
	}
	// Fields that describe the API rather than the argument: they only eat the model's context.
	for _, noise := range []string{"example", "examples", "externalDocs", "xml", "discriminator", "deprecated", "readOnly", "writeOnly"} {
		delete(m, noise)
	}
	if len(m) == 0 {
		m["type"] = "string"
	}
	return m
}

func parameterSchema(p *openapi3.Parameter) map[string]any {
	var m map[string]any
	if p.Schema != nil {
		m = asJSONSchema(p.Schema.Value)
	} else {
		m = map[string]any{"type": "string"}
	}
	// The PARAMETER's description beats the schema's: it is written for whoever calls.
	if p.Description != "" {
		m["description"] = p.Description
	}
	if _, has := m["description"]; !has {
		m["description"] = fmt.Sprintf("%s parameter (%s)", p.Name, p.In)
	}
	return m
}

// operationBody returns the body schema and HOW it travels.
//
// JSON and form-urlencoded, in that order of preference. The form is no historical footnote:
// APIs in production do take bodies that way, and a JSON-only implementation simply cannot see
// their arguments — the tool comes up with no parameters at all and nobody can call it.
func operationBody(op *openapi3.Operation) (*openapi3.Schema, string) {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil, ""
	}
	content := op.RequestBody.Value.Content
	for _, preferred := range []string{"application/json", "application/x-www-form-urlencoded"} {
		if media, ok := content[preferred]; ok && media.Schema != nil {
			return media.Schema.Value, preferred
		}
	}
	// Suffixed variants (`application/vnd.x+json`, `…;charset=utf-8`).
	for contentType, media := range content {
		if media.Schema == nil {
			continue
		}
		if strings.Contains(contentType, "json") {
			return media.Schema.Value, "application/json"
		}
		if strings.Contains(contentType, "x-www-form-urlencoded") {
			return media.Schema.Value, "application/x-www-form-urlencoded"
		}
	}
	return nil, ""
}

func bodyIsRequired(op *openapi3.Operation) bool {
	return op.RequestBody != nil && op.RequestBody.Value != nil && op.RequestBody.Value.Required
}

// requiredBodyFields only marks fields as required when the BODY itself is required — demanding
// a field of an optional body would forbid the bodyless call the spec allows.
func requiredBodyFields(op *openapi3.Operation, body *openapi3.Schema) []string {
	if !bodyIsRequired(op) {
		return nil
	}
	return body.Required
}

// operationName prefers the spec's `operationId` — it is the name whoever wrote the API chose,
// and it usually reads better than anything derived from method and path.
//
// Without one, it is built from method + path. A collision is never left silent: two tools with
// the same name would have the model call one thinking it is the other, so the second gets a
// suffix.
func operationName(op *openapi3.Operation, method, path string, used map[string]int) string {
	base := op.OperationID
	if base == "" {
		parts := []string{strings.ToLower(method)}
		for _, p := range strings.Split(path, "/") {
			p = strings.Trim(p, "{}")
			if p != "" {
				parts = append(parts, p)
			}
		}
		base = strings.Join(parts, "_")
	}
	base = sanitise(base)
	if base == "" {
		base = "operation"
	}
	used[base]++
	if n := used[base]; n > 1 {
		return fmt.Sprintf("%s_%d", base, n)
	}
	return base
}

// sanitise keeps only what a tool name accepts.
func sanitise(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ', r == '.', r == '/':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_-")
}

func describe(op *openapi3.Operation, method, path string) string {
	parts := []string{}
	if op.Summary != "" {
		parts = append(parts, op.Summary)
	}
	if op.Description != "" && op.Description != op.Summary {
		parts = append(parts, op.Description)
	}
	// Verb and path always go in: they tell the model whether the operation READS or CHANGES
	// something, and not every spec bothers to say so in prose.
	parts = append(parts, fmt.Sprintf("(%s %s)", strings.ToUpper(method), path))
	return strings.Join(parts, " — ")
}
