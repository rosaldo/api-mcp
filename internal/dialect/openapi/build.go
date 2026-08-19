package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/rosaldo/api-mcp/internal/core"
	"gopkg.in/yaml.v3"
)

func yamlUnmarshal(b []byte, v any) error { return yaml.Unmarshal(b, v) }

// bodyArg is the argument name carrying the request body when it is not an object of named
// properties (a top-level array, say). Objects become loose properties instead, which is what
// the model fills in best.
const bodyArg = "body"

// build turns one operation from the spec into a callable Operation.
//
// The separation this project is named after lives here: this function DESCRIBES (name, schema)
// and returns an `Invoke` that EXECUTES. Whoever registers the tool has no idea there is HTTP
// on the other side — which is what lets the GraphQL dialect sit alongside without touching a
// thing.
func build(base, path, method string, op *openapi3.Operation, item *openapi3.PathItem, o Options, used map[string]int) core.Operation {
	name := operationName(op, method, path, used)
	schema := core.NewObjectSchema()
	// alias → the name the API actually expects. Only names that had to be rewritten appear
	// here; `execute` uses it to put them back before the request goes out.
	aliases := map[string]string{}

	// Path-level parameters apply to every method under that path; the operation's own come
	// after and may override them — the precedence the spec itself defines.
	params := append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...)
	byName := map[string]*openapi3.Parameter{}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		p := ref.Value
		byName[p.Name] = p
		// Authentication header declared in the spec: when THIS server already authenticates,
		// asking the model for the token is worse than useless — it does not have one, and a
		// required argument nobody can fill blocks the tool. It stays in the parameter map in
		// case the model sends it anyway, but disappears from the schema.
		if o.Auth != nil && isAuthHeader(p) {
			continue
		}
		alias := register(aliases, p.Name)
		schema.Properties[alias] = parameterSchema(p)
		if p.Required {
			schema.Required = append(schema.Required, alias)
		}
	}

	body, bodyType := operationBody(op)
	if body != nil {
		if body.Type != nil && body.Type.Is("object") && len(body.Properties) > 0 {
			// A body with named properties: each one becomes a first-class argument.
			for propName, prop := range body.Properties {
				alias := register(aliases, propName)
				if _, taken := schema.Properties[alias]; taken {
					continue // a parameter with the same name already claimed the slot
				}
				schema.Properties[alias] = asJSONSchema(prop.Value)
			}
			for _, required := range requiredBodyFields(op, body) {
				schema.Required = append(schema.Required, register(aliases, required))
			}
		} else {
			schema.Properties[bodyArg] = asJSONSchema(body)
			if bodyIsRequired(op) {
				schema.Required = append(schema.Required, bodyArg)
			}
		}
	}

	return core.Operation{
		Name:        name,
		Description: describe(op, method, path),
		Input:       schema,
		Invoke: func(ctx context.Context, args map[string]any) (string, error) {
			return execute(ctx, base, path, method, byName, body, bodyType, unalias(aliases, args), o)
		},
	}
}

// register records an alias when the name had to be rewritten, and returns what the model will
// see. Names that were already valid pass through and stay out of the map.
func register(aliases map[string]string, name string) string {
	safe := safePropertyName(name)
	if safe != name {
		aliases[safe] = name
	}
	return safe
}

// unalias puts the original names back before the request is built. A name the model sent that
// is not an alias passes through untouched — it might be an argument we never rewrote.
func unalias(aliases map[string]string, args map[string]any) map[string]any {
	if len(aliases) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		if original, ok := aliases[key]; ok {
			out[original] = value
			continue
		}
		out[key] = value
	}
	return out
}

// execute assembles the request and returns the response body.
func execute(ctx context.Context, base, path, method string, params map[string]*openapi3.Parameter,
	body *openapi3.Schema, bodyType string, args map[string]any, o Options) (string, error) {

	target := base + path
	query := url.Values{}
	headers := map[string]string{}
	leftover := map[string]any{}

	for key, value := range args {
		p, isParam := params[key]
		if !isParam {
			leftover[key] = value
			continue
		}
		switch p.In {
		case "path":
			target = strings.ReplaceAll(target, "{"+key+"}", url.PathEscape(asText(value)))
		case "query":
			query.Set(key, asText(value))
		case "header":
			headers[key] = asText(value)
		case "cookie":
			headers["Cookie"] = key + "=" + asText(value)
		}
	}
	if missing := placeholders(target); len(missing) > 0 {
		return "", fmt.Errorf("missing path parameter %s", strings.Join(missing, ", "))
	}

	var payload io.Reader
	if body != nil && len(leftover) > 0 {
		var err error
		payload, err = encodeBody(leftover, bodyType)
		if err != nil {
			return "", err
		}
	}

	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), target, payload)
	if err != nil {
		return "", err
	}
	if payload != nil {
		req.Header.Set("Content-Type", bodyType)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
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
	// Capped: an API response can be huge, and its destination is a model's context window.
	// Truncating here is more honest than blowing up the caller's context.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s %s returned %s: %s", strings.ToUpper(method), target, resp.Status, firstChars(string(data), 500))
	}
	return string(data), nil
}

// encodeBody delivers the body in the format the API declared.
//
// In form-urlencoded, composite values become JSON inside the field: that is what APIs which
// take forms do when they need structure, and it beats dropping the value in silence.
func encodeBody(fields map[string]any, contentType string) (io.Reader, error) {
	if contentType == "application/x-www-form-urlencoded" {
		form := url.Values{}
		for name, value := range fields {
			form.Set(name, asText(value))
		}
		return strings.NewReader(form.Encode()), nil
	}
	var payload any = fields
	// A body that is not an object of properties travels whole in the `body` argument.
	if v, ok := fields[bodyArg]; ok && len(fields) == 1 {
		payload = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding body: %w", err)
	}
	return bytes.NewReader(b), nil
}

// isAuthHeader recognises the header that carries a credential. Only `Authorization`: API keys
// travel in headers with arbitrary names (`X-Api-Key`, `token`), and guessing which ones would
// hide from the model a parameter it actually needs to fill.
func isAuthHeader(p *openapi3.Parameter) bool {
	return p.In == "header" && strings.EqualFold(p.Name, "authorization")
}

func placeholders(u string) []string {
	var missing []string
	rest := u
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			return missing
		}
		j := strings.Index(rest[i:], "}")
		if j < 0 {
			return missing
		}
		missing = append(missing, rest[i+1:i+j])
		rest = rest[i+j:]
	}
}

func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return strings.Trim(string(b), `"`)
	}
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
