package graphql

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

	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/spec"
)

const sdl = `
schema { query: Query mutation: Mutation }
type Query {
  "Find offers from a programme"
  offers(country: String!, status: OfferStatus, limit: Int = 20): [Offer!]!
  offer(id: ID!): Offer
}
type Mutation {
  createLink(input: LinkInput!): Link!
}
enum OfferStatus { ACTIVE PAUSED }
input LinkInput { url: String! subId: String }
type Offer { id: ID! name: String! commission: Float advertiser: Advertiser secret(token: String!): String }
type Advertiser { id: ID! name: String! site: Site }
type Site { url: String! }
type Link { url: String! createdAt: String }
`

// Every Query and Mutation field becomes a tool — the counterpart of "one endpoint, one tool"
// on the REST side.
func TestEveryFieldBecomesATool(t *testing.T) {
	ops := operations(t, sdl, Options{Endpoint: "https://api.test/graphql"})
	names := map[string]string{}
	for _, o := range ops {
		names[o.Name] = o.Description
	}
	for _, want := range []string{"offers", "offer", "createLink"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing tool %q", want)
		}
	}
	if !strings.Contains(names["offers"], "Find offers") {
		t.Errorf("the schema's description was lost: %q", names["offers"])
	}
}

// What the model reads about the arguments has to carry what the schema knows: an enum becomes
// a list of values, an input object becomes an object with properties, required stays required.
func TestArgumentsBecomeFaithfulJSONSchema(t *testing.T) {
	ops := byName(t, operations(t, sdl, Options{Endpoint: "x"}))

	input := ops["offers"].Input
	if len(input.Required) != 1 || input.Required[0] != "country" {
		t.Errorf("required = %v, want [country] (limit has a default, status is optional)", input.Required)
	}
	status, _ := input.Properties["status"].(map[string]any)
	enum, _ := status["enum"].([]any)
	if len(enum) != 2 {
		t.Errorf("the OfferStatus enum never reached the model: %v", status)
	}

	linkInput, _ := ops["createLink"].Input.Properties["input"].(map[string]any)
	if linkInput["type"] != "object" {
		t.Fatalf("the input object became %v", linkInput)
	}
	props, _ := linkInput["properties"].(map[string]any)
	if _, ok := props["url"]; !ok {
		t.Errorf("the input object's properties were lost: %v", linkInput)
	}
}

// The automatic selection is what makes a GraphQL tool callable without the model writing the
// query. It takes scalars, descends one level into objects, and skips fields that need arguments.
func TestAutomaticSelection(t *testing.T) {
	var received struct{ Query string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		_, _ = w.Write([]byte(`{"data":{"offers":[]}}`))
	}))
	defer srv.Close()

	ops := byName(t, operations(t, sdl, Options{Endpoint: srv.URL, Client: srv.Client()}))
	if _, err := ops["offers"].Invoke(context.Background(), map[string]any{"country": "BR"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	q := received.Query
	for _, want := range []string{"id", "name", "commission", "advertiser {"} {
		if !strings.Contains(q, want) {
			t.Errorf("the selection did not bring %q: %s", want, q)
		}
	}
	if strings.Contains(q, "secret") {
		t.Errorf("a field requiring arguments entered the selection: %s", q)
	}
	// Arguments travel as VARIABLES: interpolating a value into the query text is injection.
	if !strings.Contains(q, "$country") {
		t.Errorf("the argument was interpolated instead of becoming a variable: %s", q)
	}
}

// GraphQL answers 200 with the errors INSIDE the body. Treating that as success would have the
// model read a failure as an empty result and carry on.
func TestErrorInsideBodyIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"unknown field: xpto"}],"data":null}`))
	}))
	defer srv.Close()

	ops := byName(t, operations(t, sdl, Options{Endpoint: srv.URL, Client: srv.Client()}))
	_, err := ops["offer"].Invoke(context.Background(), map[string]any{"id": "1"})
	if err == nil {
		t.Fatal("a GraphQL error passed as success")
	}
	if !strings.Contains(err.Error(), "xpto") {
		t.Errorf("the error does not say what the API complained about: %v", err)
	}
}

// Introspection is how a schema is known when only the endpoint is available. It has to yield
// the same tools as the equivalent SDL.
func TestIntrospectionBecomesSchema(t *testing.T) {
	intro := `{"data":{"__schema":{
	  "queryType":{"name":"Query"},
	  "types":[
	    {"kind":"OBJECT","name":"Query","fields":[
	      {"name":"ping","args":[],"type":{"kind":"SCALAR","name":"String"}},
	      {"name":"offer","args":[{"name":"id","type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}}],
	       "type":{"kind":"OBJECT","name":"Offer"}}]},
	    {"kind":"OBJECT","name":"Offer","fields":[
	      {"name":"id","args":[],"type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}},
	      {"name":"name","args":[],"type":{"kind":"SCALAR","name":"String"}}]}
	  ]}}}`
	ops := byName(t, operations(t, intro, Options{Endpoint: "https://x.test"}))
	if _, ok := ops["offer"]; !ok {
		t.Fatalf("introspection did not yield the `offer` tool (tools: %v)", keys(ops))
	}
	if len(ops["offer"].Input.Required) != 1 {
		t.Errorf("the argument's required flag was lost in conversion: %v", ops["offer"].Input.Required)
	}
}

// With no endpoint there is nowhere to send the query — and a schema, unlike an OpenAPI spec,
// does not say where the API lives.
func TestNoEndpointFailsAtStartup(t *testing.T) {
	doc := docFrom(t, sdl)
	if _, err := Operations(context.Background(), doc, Options{}); err == nil {
		t.Error("started with no endpoint")
	}
}

func operations(t *testing.T, text string, o Options) []core.Operation {
	t.Helper()
	ops, err := Operations(context.Background(), docFrom(t, text), o)
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	return ops
}

func docFrom(t *testing.T, text string) *spec.Document {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := spec.Load(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func byName(t *testing.T, ops []core.Operation) map[string]core.Operation {
	t.Helper()
	m := map[string]core.Operation{}
	for _, o := range ops {
		m[o.Name] = o
	}
	return m
}

func keys(m map[string]core.Operation) []string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	return s
}
