package discovery

import (
	"encoding/json"
	"strings"
)

// Discovery describes types the way JSON Schema does, with one difference that matters here:
// every `$ref` points into a flat `schemas` table at the top of the document, and those types
// refer to each other freely. Google's own types are deeply recursive — expanding
// `GenerateContentRequest` all the way out comes to about 60 KB, for ONE tool, in a context
// window shared with everything else the model is doing.
//
// So expansion is bounded. Past the limit a nested object becomes a plain `object`, with a note
// saying where its shape is documented. The model can still send it — JSON is JSON — it just is
// not handed a map of every field up front.

// DefaultDepth is how far `$ref` chains are followed. Two levels covers the shape of a request
// (its fields, and the fields of the objects it holds) without dragging in the whole type graph.
const DefaultDepth = 2

// jsonSchema converts one discovery type into the JSON Schema an MCP tool publishes.
func jsonSchema(table map[string]json.RawMessage, raw json.RawMessage, depth int, seen map[string]bool) map[string]any {
	var node struct {
		Ref                  string                     `json:"$ref"`
		Type                 string                     `json:"type"`
		Format               string                     `json:"format"`
		Description          string                     `json:"description"`
		Enum                 []string                   `json:"enum"`
		EnumDescriptions     []string                   `json:"enumDescriptions"`
		Items                json.RawMessage            `json:"items"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
		Required             bool                       `json:"required"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return map[string]any{"type": "object"}
	}

	if node.Ref != "" {
		// A type that refers to itself, directly or through a chain, would expand forever. The
		// depth limit alone does not catch it early enough to be cheap, so the visited set does.
		if depth <= 0 || seen[node.Ref] {
			return map[string]any{
				"type":        "object",
				"description": strings.TrimSpace(node.Description + " (" + node.Ref + " object; see the API's own reference for its fields)"),
			}
		}
		target, ok := table[node.Ref]
		if !ok {
			return map[string]any{"type": "object", "description": node.Ref}
		}
		nested := make(map[string]bool, len(seen)+1)
		for k := range seen {
			nested[k] = true
		}
		nested[node.Ref] = true
		out := jsonSchema(table, target, depth-1, nested)
		if node.Description != "" {
			out["description"] = node.Description
		}
		return out
	}

	out := map[string]any{}
	switch node.Type {
	case "integer", "number", "boolean", "string":
		out["type"] = node.Type
	case "array":
		out["type"] = "array"
		if len(node.Items) > 0 {
			out["items"] = jsonSchema(table, node.Items, depth-1, seen)
		}
	case "object", "":
		out["type"] = "object"
		if len(node.Properties) > 0 {
			if depth <= 0 {
				out["description"] = joinNonEmpty(node.Description, "(object; see the API's own reference for its fields)")
				return out
			}
			props := map[string]any{}
			var required []string
			for name, sub := range node.Properties {
				props[name] = jsonSchema(table, sub, depth-1, seen)
				if isRequired(sub) {
					required = append(required, name)
				}
			}
			out["properties"] = props
			if len(required) > 0 {
				out["required"] = required
			}
		} else if len(node.AdditionalProperties) > 0 && depth > 0 {
			out["additionalProperties"] = jsonSchema(table, node.AdditionalProperties, depth-1, seen)
		}
	default:
		// `any`, and anything a future revision invents: an open object is the honest answer.
		out["type"] = "object"
	}

	if node.Description != "" {
		out["description"] = node.Description
	}
	// The format is worth carrying: `int64` arrives as a STRING in Google's APIs, and a model
	// that does not know that sends a number and gets a 400 it cannot explain.
	if node.Format != "" {
		out["format"] = node.Format
	}
	if len(node.Enum) > 0 {
		out["enum"] = node.Enum
		if d := describeEnum(node.Enum, node.EnumDescriptions); d != "" {
			out["description"] = joinNonEmpty(node.Description, d)
		}
	}
	return out
}

// describeEnum folds `enumDescriptions` into the description, because JSON Schema has nowhere to
// put a per-value explanation and the values alone are often opaque (`FULL`, `BASIC`, `MINIMAL`).
func describeEnum(values, descriptions []string) string {
	if len(descriptions) != len(values) {
		return ""
	}
	var parts []string
	for i, v := range values {
		if d := strings.TrimSpace(descriptions[i]); d != "" {
			parts = append(parts, v+": "+d)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func isRequired(raw json.RawMessage) bool {
	var n struct {
		Required bool `json:"required"`
	}
	return json.Unmarshal(raw, &n) == nil && n.Required
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, " ")
}
