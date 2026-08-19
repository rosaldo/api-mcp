package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosaldo/api-mcp/internal/spec"
)

// A spec shaped like a real one, not an example written to pass: it combines three things that
// each break an integration on their own — OpenAPI 3.1, served as YAML, and auth by short-lived
// token. Toy specs catch none of it.
func TestRealShapedSpecInYAML(t *testing.T) {
	doc := loadFile(t, "testdata/openapi31-form-token.yaml")
	if doc.Kind != spec.KindOpenAPI {
		t.Fatalf("Kind = %q", doc.Kind)
	}
	ops, err := Operations(context.Background(), doc, Options{})
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if len(ops) != 6 {
		t.Errorf("produced %d operations, the spec has 6 endpoints", len(ops))
	}

	byName := map[string]bool{}
	for _, o := range ops {
		byName[o.Name] = true
	}
	for _, want := range []string{"authenticate", "itemsAll", "linkGenerate", "reportById"} {
		if !byName[want] {
			t.Errorf("missing operation %q — names: %v", want, keys(byName))
		}
	}
}

// The schema has to reach the model AS IT IS in the spec. Passing only `type` throws away enum,
// format and structure — and every lost field is a call the model will have to guess at.
func TestSchemaArrivesWhole(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.yaml")
	os.WriteFile(p, []byte(`
openapi: "3.0.3"
servers: [{url: "https://api.exemplo.test"}]
paths:
  /offers:
    get:
      operationId: listar
      parameters:
        - name: status
          in: query
          required: true
          description: filter by state
          schema: {type: string, enum: [active, paused, ended]}
        - name: limit
          in: query
          schema: {type: integer, minimum: 1, maximum: 200, default: 20}
      responses: {"200": {description: ok}}
`), 0o644)

	ops, err := Operations(context.Background(), loadFile(t, p), Options{})
	if err != nil {
		t.Fatal(err)
	}
	input := ops[0].Input

	status, _ := input.Properties["status"].(map[string]any)
	if status["enum"] == nil {
		t.Errorf("the enum was lost on the way: %v", status)
	}
	if status["description"] != "filter by state" {
		t.Errorf("the parameter description was lost: %v", status)
	}
	limit, _ := input.Properties["limit"].(map[string]any)
	if limit["maximum"] == nil || limit["minimum"] == nil {
		t.Errorf("the numeric bounds were lost: %v", limit)
	}
	if len(input.Required) != 1 || input.Required[0] != "status" {
		t.Errorf("required = %v, want [status]", input.Required)
	}
}

// Actually executing: path parameter substituted, query assembled, JSON body, auth applied.
// This is the test that proves an operation is more than a pretty description.
func TestExecutesTheCall(t *testing.T) {
	var seen struct {
		method, path, query, auth, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen.method, seen.path, seen.query = r.Method, r.URL.Path, r.URL.RawQuery
		seen.auth, seen.body = r.Header.Get("Authorization"), string(raw)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	os.WriteFile(p, []byte(`{"openapi":"3.0.0","servers":[{"url":"`+srv.URL+`"}],
	 "paths":{"/stores/{id}/links":{"post":{"operationId":"createLink",
	   "parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
	                 {"name":"country","in":"query","schema":{"type":"string"}}],
	   "requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object",
	     "properties":{"url":{"type":"string"}},"required":["url"]}}}},
	   "responses":{"200":{"description":"ok"}}}}}}`), 0o644)

	ops, err := Operations(context.Background(), loadFile(t, p), Options{
		Auth: fixedBearer{"tok-123"}, Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ops[0].Invoke(context.Background(), map[string]any{
		"id": "42", "country": "BR", "url": "https://store.test/item",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if seen.path != "/stores/42/links" {
		t.Errorf("path = %q — the path parameter was not substituted", seen.path)
	}
	if seen.query != "country=BR" {
		t.Errorf("query = %q", seen.query)
	}
	if seen.auth != "Bearer tok-123" {
		t.Errorf("auth = %q", seen.auth)
	}
	if !strings.Contains(seen.body, "store.test/item") {
		t.Errorf("body = %q — it was not assembled", seen.body)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("response = %q", out)
	}
}

// A missing path parameter must not become a call to a URL with `{id}` still in it: the API
// would answer 404 and the model would go hunting for a bug that is not its own.
func TestMissingPathParamFailsBeforeCalling(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	os.WriteFile(p, []byte(`{"openapi":"3.0.0","servers":[{"url":"https://api.test"}],
	 "paths":{"/x/{id}":{"get":{"operationId":"fetch","parameters":[
	   {"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
	   "responses":{"200":{"description":"ok"}}}}}}`), 0o644)

	ops, _ := Operations(context.Background(), loadFile(t, p), Options{})
	if _, err := ops[0].Invoke(context.Background(), map[string]any{}); err == nil {
		t.Error("called without the path parameter")
	} else if !strings.Contains(err.Error(), "id") {
		t.Errorf("the error does not say which parameter was missing: %v", err)
	}
}

type fixedBearer struct{ token string }

func (b fixedBearer) Apply(_ context.Context, r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return nil
}

func loadFile(t *testing.T, path string) *spec.Document {
	t.Helper()
	doc, err := spec.Load(context.Background(), path, "")
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	return doc
}

func keys(m map[string]bool) []string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	return s
}

var _ = json.Marshal

// Form-urlencoded bodies. There are production APIs that take EVERY body that way, and without
// support for it the tool comes up with no parameters at all: the model cannot call it. That is
// how this defect surfaced here — by measuring the published tool, not by reading the spec.
func TestFormUrlencodedBody(t *testing.T) {
	var seen struct{ contentType, body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen.contentType, seen.body = r.Header.Get("Content-Type"), string(raw)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"openapi":"3.0.0","servers":[{"url":"`+srv.URL+`"}],
	 "paths":{"/deeplink":{"post":{"operationId":"deeplink",
	   "requestBody":{"required":true,"content":{"application/x-www-form-urlencoded":{"schema":{
	     "type":"object","properties":{"url":{"type":"string"},"offer_id":{"type":"integer"}},
	     "required":["url","offer_id"]}}}},
	   "responses":{"200":{"description":"ok"}}}}}}`), 0o644)

	ops, err := Operations(context.Background(), loadFile(t, p), Options{Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ops[0].Input.Properties["url"]; !ok {
		t.Fatalf("the body did not become arguments: %v", ops[0].Input.Properties)
	}
	if _, err := ops[0].Invoke(context.Background(), map[string]any{"url": "https://store.test/p", "offer_id": 42}); err != nil {
		t.Fatal(err)
	}
	if seen.contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", seen.contentType)
	}
	if !strings.Contains(seen.body, "offer_id=42") || !strings.Contains(seen.body, "url=https") {
		t.Errorf("body = %q — it was not form-encoded", seen.body)
	}
}

// An auth header declared in the spec: with authentication configured it DISAPPEARS from the
// schema (the model has no token, and an unfillable required argument blocks the tool); with no
// authentication it stays — then it is the model that has to fill it.
func TestSpecAuthorizationDisappearsWhenWeAuthenticate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"openapi":"3.0.0","servers":[{"url":"https://api.test"}],
	 "paths":{"/x":{"get":{"operationId":"x","parameters":[
	   {"name":"Authorization","in":"header","required":true,"schema":{"type":"string"}},
	   {"name":"country","in":"query","schema":{"type":"string"}}],
	   "responses":{"200":{"description":"ok"}}}}}}`), 0o644)

	withAuth, err := Operations(context.Background(), loadFile(t, p), Options{Auth: fixedBearer{"t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := withAuth[0].Input.Properties["Authorization"]; has {
		t.Error("asked the model for the token while authenticating on its own")
	}
	if len(withAuth[0].Input.Required) != 0 {
		t.Errorf("an unfillable required argument survived: %v", withAuth[0].Input.Required)
	}

	withoutAuth, err := Operations(context.Background(), loadFile(t, p), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := withoutAuth[0].Input.Properties["Authorization"]; !has {
		t.Error("with no authentication configured the header must stay — nobody else will set it")
	}
}
