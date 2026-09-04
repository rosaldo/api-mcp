package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/rosaldo/api-mcp/internal/auth"
)

// A secret in a flag is a secret in `ps` and in /proc/<pid>/cmdline, readable by every other
// process on the machine — including whatever the model decides to run. `env:NAME` is how a
// value reaches the flag without ever appearing in the arguments.
func TestEnvPrefixKeepsSecretsOutOfArguments(t *testing.T) {
	t.Setenv("API_MCP_TEST_SECRET", "s3cr3t")

	got, err := fromEnv("env:API_MCP_TEST_SECRET")
	if err != nil {
		t.Fatalf("fromEnv: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("fromEnv = %q, want the variable's value", got)
	}

	// A plain value stays a plain value — the prefix is opt-in.
	if got, err := fromEnv("plain-token"); err != nil || got != "plain-token" {
		t.Errorf("fromEnv(plain) = %q, %v", got, err)
	}
}

// An unset variable must fail loudly. Resolving to "" would authenticate with an empty token
// and fail somewhere that says nothing about a missing variable.
func TestUnsetVariableFailsLoudly(t *testing.T) {
	_, err := fromEnv("env:API_MCP_DEFINITELY_NOT_SET")
	if err == nil {
		t.Fatal("an unset variable resolved silently")
	}
	if !strings.Contains(err.Error(), "API_MCP_DEFINITELY_NOT_SET") {
		t.Errorf("the error does not name the variable: %v", err)
	}
}

// Every credential path has to honour it, not just --bearer: the auth flow's fields carry the
// key and the secret themselves.
func TestEveryCredentialPathResolvesEnv(t *testing.T) {
	t.Setenv("TEST_KEY", "k-123")
	t.Setenv("TEST_SECRET", "s-456")

	// chooseAuth and not buildAuth: what is under test here is env: resolution, and buildAuth
	// wraps the result in the chain that fills {uuid}, which has no concrete type to inspect.
	flow, err := chooseAuth(config{
		flowURL:    "https://api.example.test/authenticate",
		flowFields: list{"key=env:TEST_KEY", "secret=env:TEST_SECRET"},
		tokenPath:  "data.token",
	})
	if err != nil {
		t.Fatalf("buildAuth flow: %v", err)
	}
	f, ok := flow.(*auth.Flow)
	if !ok {
		t.Fatalf("expected *auth.Flow, got %T", flow)
	}
	if f.Fields["key"] != "k-123" || f.Fields["secret"] != "s-456" {
		t.Errorf("--auth-field values not resolved: %v", f.Fields)
	}

	bearer, err := buildAuth(config{authKind: "bearer", bearer: "env:TEST_KEY"})
	if err != nil {
		t.Fatalf("buildAuth bearer: %v", err)
	}
	if !strings.Contains(stamped(t, bearer), "k-123") {
		t.Error("--bearer did not resolve env:")
	}

	apikey, err := buildAuth(config{authKind: "apikey", apiKey: "header:X-Key=env:TEST_SECRET"})
	if err != nil {
		t.Fatalf("buildAuth apikey: %v", err)
	}
	if !strings.Contains(stamped(t, apikey), "s-456") {
		t.Error("--api-key did not resolve env:")
	}
}

// stamped runs the applier over a request and returns everything it wrote, so a test can assert
// on the resolved value without the package having to expose it.
func stamped(t *testing.T, a auth.Applier) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(context.Background(), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dump := req.URL.String()
	for name, values := range req.Header {
		dump += " " + name + ":" + values[0]
	}
	return dump
}

// TestEveryFlagIsInTheReadme keeps the flag table from drifting, in both directions.
//
// It caught a real drift the day it was written: `--type` still advertised two dialects out of
// three, `--depth` was documented under the GraphQL-only name it used to have, and `--blob-dir`
// was missing altogether. Nothing broke — the tool worked, the README simply described a version
// of it that no longer existed, and only a reader would ever have found out.
//
// One side reads the REAL FlagSet, the other reads the table. Neither can move alone.
func TestEveryFlagIsInTheReadme(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	table := flagTable(t, string(readme))

	fs := flag.NewFlagSet("api-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs)

	declared := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		declared[f.Name] = true
		if !table[f.Name] {
			t.Errorf("--%s exists and is not in the README's flag table", f.Name)
		}
	})
	for name := range table {
		if !declared[name] {
			t.Errorf("the README documents --%s, which no longer exists", name)
		}
	}
	if len(table) == 0 {
		t.Fatal("no flags found in the README table: the test stopped measuring what it promises")
	}
}

// flagTable pulls the flag names out of the `## All flags` section. Names may share a cell
// (`--addr`, `--path`), so every one in the first column counts.
func flagTable(t *testing.T, readme string) map[string]bool {
	t.Helper()
	start := strings.Index(readme, "## All flags")
	if start < 0 {
		t.Fatal("the README has no `## All flags` section")
	}
	section := readme[start:]
	if end := strings.Index(section[1:], "\n## "); end > 0 {
		section = section[:end]
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile("`--([a-z-]+)`").FindAllStringSubmatch(section, -1) {
		names[m[1]] = true
	}
	return names
}
