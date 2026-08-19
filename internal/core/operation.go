// Package core defines what every dialect produces and what the server consumes.
//
// An Operation is something you can call: it has a name, a description, a schema for what it
// takes, and a function that runs it. It is deliberately poor — nothing in it says whether
// underneath there is a GET with a query string, a POST with a form, or a GraphQL query.
//
// That poverty is the point. The MCP server registers Operations without knowing where they
// came from, so a new dialect (gRPC-Web, SOAP, whatever) arrives as a translator and nothing
// else changes.
//
// What the boundary buys: DESCRIBING an operation and EXECUTING it are different jobs, and
// keeping them apart is what lets dialects of different natures live side by side. Merge them
// into one function and every new dialect becomes a branch in the middle of everyone's path.
package core

import "context"

// Operation is a call the model can make, already translated from its source dialect.
type Operation struct {
	// Name identifies the tool in MCP. Unique within a server.
	Name string
	// Description is what the model reads to decide whether this is the right operation.
	Description string
	// Input describes the arguments in JSON Schema — the contract the model fills in.
	Input Schema
	// Invoke runs it, with arguments already validated against Input.
	Invoke func(ctx context.Context, args map[string]any) (string, error)
}

// Schema is the subset of JSON Schema that describes an operation's arguments.
//
// A subset rather than the full standard because the destination is an MCP tool's
// `inputSchema` field, and what travels there is an object with properties. Whatever a dialect
// knows beyond that (format, enum, array items) fits inside Properties, which is open on purpose.
type Schema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
}

// NewObjectSchema returns an empty object schema — the starting point for every operation,
// including those that take no arguments at all (the model still needs an object, even empty).
func NewObjectSchema() Schema {
	return Schema{Type: "object", Properties: map[string]any{}}
}
