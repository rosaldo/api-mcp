package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/rosaldo/api-mcp/internal/auth"
	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/spec"
	"github.com/vektah/gqlparser/v2/ast"
)

// Options are decisions made by whoever starts the server.
type Options struct {
	Endpoint string // where queries go (required: the schema does not say)
	Auth     auth.Applier
	Client   *http.Client
	Headers  map[string]string
	Depth    int // how deep the automatic selection descends; 0 uses the default
}

// defaultDepth: two levels reach the object and the objects immediately inside it, which is
// where nearly every useful answer lives. Deeper makes huge responses and cycles; shallower
// leaves out what gives the record meaning (the price inside the offer, the name inside the
// store).
const defaultDepth = 2

// selectionArg is the escape hatch: when the automatic selection does not fit, the model writes
// its own.
const selectionArg = "_select"

// Operations translates the whole schema: every Query and Mutation field becomes a tool.
func Operations(_ context.Context, doc *spec.Document, o Options) ([]core.Operation, error) {
	if o.Endpoint == "" {
		return nil, fmt.Errorf("a GraphQL schema does not say where the API lives: pass --endpoint")
	}
	schema, err := loadSchema(doc.Raw)
	if err != nil {
		return nil, err
	}
	if o.Auth == nil {
		o.Auth = auth.None{}
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}
	if o.Depth <= 0 {
		o.Depth = defaultDepth
	}

	var ops []core.Operation
	for _, root := range []struct {
		def  *ast.Definition
		kind string
	}{{schema.Query, "query"}, {schema.Mutation, "mutation"}} {
		if root.def == nil {
			continue
		}
		fields := append(ast.FieldList{}, root.def.Fields...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		for _, field := range fields {
			if strings.HasPrefix(field.Name, "__") {
				continue // GraphQL's own introspection is not an operation of the API
			}
			ops = append(ops, build(schema, field, root.kind, o))
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("the schema has no Query or Mutation with fields")
	}
	return ops, nil
}

func build(schema *ast.Schema, field *ast.FieldDefinition, rootKind string, o Options) core.Operation {
	input := core.NewObjectSchema()
	for _, arg := range field.Arguments {
		input.Properties[arg.Name] = argumentSchema(schema, arg)
		if arg.Type.NonNull && arg.DefaultValue == nil {
			input.Required = append(input.Required, arg.Name)
		}
	}
	// The default selection is already assembled; this argument exists for when it does not fit.
	input.Properties[selectionArg] = map[string]any{
		"type": "string",
		"description": "optional — GraphQL selection body, without the outer braces " +
			"(e.g. `id name price { amount currency }`). Empty means the default selection.",
	}

	defaultSelection := selectionFor(schema, field.Type, o.Depth)
	description := field.Description
	if description == "" {
		description = fmt.Sprintf("%s %s", rootKind, field.Name)
	}
	description += fmt.Sprintf(" — returns %s", field.Type.String())

	return core.Operation{
		Name:        field.Name,
		Description: description,
		Input:       input,
		Invoke: func(ctx context.Context, args map[string]any) (string, error) {
			return execute(ctx, field, rootKind, defaultSelection, args, o)
		},
	}
}

// execute assembles the GraphQL document and sends it.
//
// Arguments travel as VARIABLES, never interpolated into the query text: interpolation is the
// road to injection — a value with quotes or braces would rewrite the query — and it also loses
// the typing the server uses to validate.
func execute(ctx context.Context, field *ast.FieldDefinition, rootKind, defaultSelection string,
	args map[string]any, o Options) (string, error) {

	selection := defaultSelection
	if s, ok := args[selectionArg].(string); ok && strings.TrimSpace(s) != "" {
		selection = strings.TrimSpace(s)
	}

	variables := map[string]any{}
	var declarations, passthrough []string
	for _, arg := range field.Arguments {
		value, given := args[arg.Name]
		if !given {
			continue
		}
		variables[arg.Name] = value
		declarations = append(declarations, fmt.Sprintf("$%s: %s", arg.Name, arg.Type.String()))
		passthrough = append(passthrough, fmt.Sprintf("%s: $%s", arg.Name, arg.Name))
	}

	var doc strings.Builder
	doc.WriteString(rootKind)
	if len(declarations) > 0 {
		doc.WriteString("(" + strings.Join(declarations, ", ") + ")")
	}
	doc.WriteString(" { " + field.Name)
	if len(passthrough) > 0 {
		doc.WriteString("(" + strings.Join(passthrough, ", ") + ")")
	}
	if selection != "" {
		doc.WriteString(" { " + selection + " }")
	}
	doc.WriteString(" }")

	body, err := json.Marshal(map[string]any{"query": doc.String(), "variables": variables})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	if err := o.Auth.Apply(ctx, req); err != nil {
		return "", err
	}

	resp, err := o.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", o.Endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s returned %s: %s", o.Endpoint, resp.Status, firstChars(string(data), 500))
	}
	// GraphQL answers 200 WITH errors inside the body — returning that as success would have
	// the model read a failure as an empty result.
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return "", fmt.Errorf("GraphQL error: %s", strings.Join(msgs, "; "))
	}
	return string(data), nil
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
