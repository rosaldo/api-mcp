package graphql

import (
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// selectionFor assembles the selection body for a return type.
//
// The rule: take the type's scalar (and enum) fields, and descend into objects while depth
// remains. Fields that require arguments are left out — calling them with none is a certain
// error, and guessing a value is worse.
//
// A type with no scalar fields at all yields an empty selection, and the call fails at the
// server with a clear message. That beats inventing `__typename` just to make the query pass: a
// response carrying no data is a silent failure, and silent failure is what we are avoiding.
func selectionFor(schema *ast.Schema, t *ast.Type, depth int) string {
	if depth <= 0 || t == nil {
		return ""
	}
	def := schema.Types[t.Name()]
	if def == nil {
		return ""
	}
	// A scalar or enum at the top: there is nothing to select, the query returns the value.
	if def.Kind == ast.Scalar || def.Kind == ast.Enum {
		return ""
	}

	var parts []string
	for _, field := range def.Fields {
		if strings.HasPrefix(field.Name, "__") || len(field.Arguments) > 0 {
			continue
		}
		child := schema.Types[field.Type.Name()]
		if child == nil {
			continue
		}
		switch child.Kind {
		case ast.Scalar, ast.Enum:
			parts = append(parts, field.Name)
		case ast.Object, ast.Interface:
			if inner := selectionFor(schema, field.Type, depth-1); inner != "" {
				parts = append(parts, field.Name+" { "+inner+" }")
			}
		}
	}
	return strings.Join(parts, " ")
}

// argumentSchema translates the argument's GraphQL type into JSON Schema, which is what the
// model reads. An enum becomes a list of allowed values — the single piece of information that
// prevents the most wrong calls.
func argumentSchema(schema *ast.Schema, arg *ast.ArgumentDefinition) map[string]any {
	m := typeToJSONSchema(schema, arg.Type, 0)
	if arg.Description != "" {
		m["description"] = arg.Description
	}
	if arg.DefaultValue != nil {
		m["default"] = arg.DefaultValue.String()
	}
	return m
}

func typeToJSONSchema(schema *ast.Schema, t *ast.Type, level int) map[string]any {
	if t == nil || level > 4 { // guard against input types that reference themselves
		return map[string]any{"type": "string"}
	}
	if t.Elem != nil {
		return map[string]any{"type": "array", "items": typeToJSONSchema(schema, t.Elem, level+1)}
	}
	switch t.NamedType {
	case "Int":
		return map[string]any{"type": "integer"}
	case "Float":
		return map[string]any{"type": "number"}
	case "Boolean":
		return map[string]any{"type": "boolean"}
	case "String", "ID":
		return map[string]any{"type": "string"}
	}

	def := schema.Types[t.NamedType]
	if def == nil {
		return map[string]any{"type": "string"}
	}
	switch def.Kind {
	case ast.Enum:
		values := make([]any, 0, len(def.EnumValues))
		for _, v := range def.EnumValues {
			values = append(values, v.Name)
		}
		return map[string]any{"type": "string", "enum": values}
	case ast.InputObject:
		props := map[string]any{}
		var required []string
		for _, field := range def.Fields {
			props[field.Name] = typeToJSONSchema(schema, field.Type, level+1)
			if field.Type.NonNull && field.DefaultValue == nil {
				required = append(required, field.Name)
			}
		}
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	default:
		// Custom scalar (Date, JSON, Upload…): a string is what the model can produce.
		return map[string]any{"type": "string", "description": "scalar " + def.Name}
	}
}
