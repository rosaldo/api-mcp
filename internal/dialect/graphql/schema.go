// Package graphql translates a GraphQL schema into callable operations.
//
// The translation differs from REST in one fundamental way, and that decides the design: in
// REST, calling an endpoint returns a whole document; in GraphQL, **the caller chooses what
// comes back**. There is no "call it and see what arrives" — without a field selection the
// server rejects the request.
//
// So each Query/Mutation field becomes a tool whose selection WE assemble: the scalar fields of
// the return type, descending while it is worth it. An optional argument lets the model ask for
// something else when the default does not fit. The common alternative — exposing one
// `graphql(query)` tool and letting the model write the whole query — hands it work the schema
// already answers, and that is where this kind of integration usually goes wrong.
package graphql

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// loadSchema accepts both forms a GraphQL schema comes in: SDL (the schema text) and the JSON
// of an introspection query. Whoever has access to the API's repository usually has the first;
// whoever only has the endpoint, the second.
func loadSchema(raw []byte) (*ast.Schema, error) {
	if isIntrospection(raw) {
		sdl, err := introspectionToSDL(raw)
		if err != nil {
			return nil, err
		}
		raw = []byte(sdl)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema", Input: string(raw)})
	if err != nil {
		return nil, fmt.Errorf("reading GraphQL schema: %w", err)
	}
	return schema, nil
}

func isIntrospection(raw []byte) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	if _, ok := doc["__schema"]; ok {
		return true
	}
	if data, ok := doc["data"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(data, &inner); err == nil {
			_, ok := inner["__schema"]
			return ok
		}
	}
	return false
}

// --- introspection → SDL ------------------------------------------------------------------
//
// Converting rather than reading the JSON directly: this way there is ONE path from here on
// (gqlparser's ast.Schema) instead of two implementations of the same understanding, drifting
// apart at the first subtlety.

type introType struct {
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Fields      []introField `json:"fields"`
	InputFields []introArg   `json:"inputFields"`
	EnumValues  []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"enumValues"`
	Interfaces []introRef `json:"interfaces"`
}

type introField struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Args        []introArg `json:"args"`
	Type        introRef   `json:"type"`
}

type introArg struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Type         introRef `json:"type"`
	DefaultValue *string  `json:"defaultValue"`
}

type introRef struct {
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	OfType *introRef `json:"ofType"`
}

// text renders the type the way SDL writes it: `[Offer!]!`, `String`, `ID!`.
func (r introRef) text() string {
	switch r.Kind {
	case "NON_NULL":
		if r.OfType != nil {
			return r.OfType.text() + "!"
		}
	case "LIST":
		if r.OfType != nil {
			return "[" + r.OfType.text() + "]"
		}
	}
	if r.Name != "" {
		return r.Name
	}
	return "String"
}

func introspectionToSDL(raw []byte) (string, error) {
	var envelope struct {
		Data *struct {
			Schema json.RawMessage `json:"__schema"`
		} `json:"data"`
		Schema json.RawMessage `json:"__schema"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("reading introspection: %w", err)
	}
	rawSchema := envelope.Schema
	if rawSchema == nil && envelope.Data != nil {
		rawSchema = envelope.Data.Schema
	}
	if rawSchema == nil {
		return "", fmt.Errorf("introspection without __schema")
	}

	var s struct {
		QueryType    *struct{ Name string } `json:"queryType"`
		MutationType *struct{ Name string } `json:"mutationType"`
		Types        []introType            `json:"types"`
	}
	if err := json.Unmarshal(rawSchema, &s); err != nil {
		return "", fmt.Errorf("reading __schema: %w", err)
	}

	var b strings.Builder
	queryName, mutationName := "Query", "Mutation"
	if s.QueryType != nil && s.QueryType.Name != "" {
		queryName = s.QueryType.Name
	}
	if s.MutationType != nil && s.MutationType.Name != "" {
		mutationName = s.MutationType.Name
	}
	fmt.Fprintf(&b, "schema { query: %s", queryName)
	if s.MutationType != nil {
		fmt.Fprintf(&b, " mutation: %s", mutationName)
	}
	b.WriteString(" }\n")

	for _, t := range s.Types {
		// GraphQL's own internals (`__Type`, `__Schema`) are not part of the API.
		if strings.HasPrefix(t.Name, "__") || t.Name == "" {
			continue
		}
		switch t.Kind {
		case "OBJECT", "INTERFACE":
			keyword := "type"
			if t.Kind == "INTERFACE" {
				keyword = "interface"
			}
			fmt.Fprintf(&b, "%s %s {\n", keyword, t.Name)
			for _, f := range t.Fields {
				b.WriteString("  " + f.Name)
				if len(f.Args) > 0 {
					args := make([]string, 0, len(f.Args))
					for _, a := range f.Args {
						args = append(args, a.Name+": "+a.Type.text())
					}
					b.WriteString("(" + strings.Join(args, ", ") + ")")
				}
				b.WriteString(": " + f.Type.text() + "\n")
			}
			b.WriteString("}\n")
		case "INPUT_OBJECT":
			fmt.Fprintf(&b, "input %s {\n", t.Name)
			for _, f := range t.InputFields {
				fmt.Fprintf(&b, "  %s: %s\n", f.Name, f.Type.text())
			}
			b.WriteString("}\n")
		case "ENUM":
			fmt.Fprintf(&b, "enum %s {\n", t.Name)
			for _, v := range t.EnumValues {
				fmt.Fprintf(&b, "  %s\n", v.Name)
			}
			b.WriteString("}\n")
		case "SCALAR":
			if !isBuiltinScalar(t.Name) {
				fmt.Fprintf(&b, "scalar %s\n", t.Name)
			}
		case "UNION":
			// No members in a minimal introspection; declaring an empty one would break the
			// parse. Skipping a union costs the tools that return it, not the whole schema.
		}
	}
	return b.String(), nil
}

func isBuiltinScalar(n string) bool {
	switch n {
	case "String", "Int", "Float", "Boolean", "ID":
		return true
	}
	return false
}
