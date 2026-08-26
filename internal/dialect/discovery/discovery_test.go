package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/spec"
)

// A miniature of the real thing: nested resources, both path templates, a query parameter that
// repeats, and a request body behind a $ref.
const doc = `{
  "kind": "discovery#restDescription",
  "name": "demo", "version": "v1",
  "baseUrl": "%s/v1/",
  "schemas": {
    "File": {
      "type": "object",
      "properties": {
        "name": {"type": "string", "description": "The name.", "required": true},
        "parent": {"$ref": "File", "description": "Recursive on purpose."}
      }
    }
  },
  "resources": {
    "files": {
      "methods": {
        "get": {
          "id": "demo.files.get", "path": "files/{fileId}", "httpMethod": "GET",
          "description": "Reads one file.",
          "parameters": {
            "fileId": {"type": "string", "location": "path", "required": true},
            "fields": {"type": "string", "location": "query"},
            "tags": {"type": "string", "location": "query", "repeated": true}
          }
        },
        "create": {
          "id": "demo.files.create", "path": "files", "httpMethod": "POST",
          "description": "Creates a file.",
          "request": {"$ref": "File"}
        }
      },
      "resources": {
        "permissions": {
          "methods": {
            "list": {"id": "demo.files.permissions.list", "path": "files/{fileId}/permissions", "httpMethod": "GET",
                     "parameters": {"fileId": {"type": "string", "location": "path", "required": true}}}
          }
        }
      }
    },
    "operations": {
      "methods": {
        "get": {
          "id": "demo.operations.get", "path": "{+name}", "httpMethod": "GET",
          "description": "Reads an operation by its full name.",
          "parameters": {"name": {"type": "string", "location": "path", "required": true}}
        }
      }
    }
  }
}`

func serve(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *spec.Document) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, &spec.Document{Raw: []byte(strings.Replace(doc, "%s", srv.URL, 1)), Kind: spec.KindDiscovery}
}

func TestNestedResourcesAllBecomeTools(t *testing.T) {
	_, d := serve(t, func(w http.ResponseWriter, r *http.Request) {})
	ops, err := Operations(context.Background(), d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range ops {
		names = append(names, o.Name)
	}
	want := []string{"files_create", "files_get", "files_permissions_list", "operations_get"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// The service name is dropped from the tool name: the server already IS this API.
func TestToolNameDropsTheServicePrefix(t *testing.T) {
	if got := toolName("drive.files.get"); got != "files_get" {
		t.Errorf("got %q, want files_get", got)
	}
	if got := toolName("compute.instances.aggregatedList"); got != "instances_aggregatedList" {
		t.Errorf("got %q, want instances_aggregatedList", got)
	}
}

// `{+name}` is reserved expansion: the value carries slashes and they must survive. Escaping them
// turns `models/x/operations/y` into a 404 that reads like the resource does not exist.
func TestReservedTemplateKeepsSlashes(t *testing.T) {
	var seen string
	srv, d := serve(t, func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: Go decodes `%2F` back into `/` on the way in, so reading Path
		// would show the slash surviving even when it had been escaped — the test would pass
		// against the very bug it exists to catch.
		seen = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	_ = srv
	ops, err := Operations(context.Background(), d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	op := find(t, ops, "operations_get")
	if _, err := op.Invoke(context.Background(), map[string]any{"name": "models/veo/operations/abc"}); err != nil {
		t.Fatal(err)
	}
	if want := "/v1/models/veo/operations/abc"; seen != want {
		t.Errorf("path = %q, want %q", seen, want)
	}
}

// And the plain template does the opposite: a value with a slash is one segment, escaped.
func TestPlainTemplateEscapes(t *testing.T) {
	var seen string
	_, d := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{}`))
	})
	ops, _ := Operations(context.Background(), d, Options{})
	op := find(t, ops, "files_get")
	if _, err := op.Invoke(context.Background(), map[string]any{"fileId": "a/b"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "a%2Fb") {
		t.Errorf("path = %q, expected the slash escaped", seen)
	}
}

func TestRepeatedQueryParameterIsRepeated(t *testing.T) {
	var raw string
	_, d := serve(t, func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	})
	ops, _ := Operations(context.Background(), d, Options{})
	op := find(t, ops, "files_get")
	if _, err := op.Invoke(context.Background(), map[string]any{
		"fileId": "1", "tags": []any{"a", "b"}, "fields": "name",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(raw, "tags=") != 2 {
		t.Errorf("query = %q, expected tags twice — joining them changes what the API receives", raw)
	}
}

// Body fields are merged into the same flat object as the parameters: one object for the model to
// fill, not a decision about what goes where.
func TestBodyFieldsBecomeArgumentsAndAreSentAsJSON(t *testing.T) {
	var body map[string]any
	_, d := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	})
	ops, _ := Operations(context.Background(), d, Options{})
	op := find(t, ops, "files_create")
	if _, ok := op.Input.Properties["name"]; !ok {
		t.Fatalf("the body's fields did not become arguments: %v", op.Input.Properties)
	}
	if _, err := op.Invoke(context.Background(), map[string]any{"name": "report.pdf"}); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "report.pdf" {
		t.Errorf("body = %v, expected the field to travel as JSON", body)
	}
}

// A type that contains itself must not expand forever, and must not be expanded far even when it
// terminates: this is the difference between a 4 KB tool and a 60 KB one.
func TestRecursiveTypeIsBounded(t *testing.T) {
	_, d := serve(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, depth := range []int{1, 2, 4} {
		ops, err := Operations(context.Background(), d, Options{Depth: depth})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(find(t, ops, "files_create").Input)
		if len(raw) > 4000 {
			t.Errorf("depth %d produced %d bytes — the recursion is not bounded", depth, len(raw))
		}
	}
}

func TestFiltersCutTheSurface(t *testing.T) {
	_, d := serve(t, func(w http.ResponseWriter, r *http.Request) {})
	ops, err := Operations(context.Background(), d, Options{
		IncludePaths:   []*regexp.Regexp{regexp.MustCompile(`^/files`)},
		ExcludeMethods: []string{"POST"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range ops {
		names = append(names, o.Name)
	}
	want := "files_get,files_permissions_list"
	if strings.Join(names, ",") != want {
		t.Errorf("tools = %v, want %s — include-paths reads the path, exclude-methods the verb", names, want)
	}
}

func find(t *testing.T, ops []core.Operation, name string) core.Operation {
	t.Helper()
	for _, o := range ops {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("tool %q not found", name)
	return core.Operation{}
}
