// api-mcp serves any API as an MCP server: point it at the specification and the tools exist.
//
// It reads OpenAPI 3.x, Swagger 2.0 and GraphQL — as JSON, YAML or SDL, from a file, a URL or
// stdin — and publishes one tool per operation. It was born out of needing to reach APIs with
// no MCP server at all, and out of refusing to hand credentials to a third party's server to
// get there.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/rosaldo/api-mcp/internal/auth"
	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/dialect/discovery"
	"github.com/rosaldo/api-mcp/internal/dialect/graphql"
	"github.com/rosaldo/api-mcp/internal/dialect/openapi"
	"github.com/rosaldo/api-mcp/internal/mcpserver"
	"github.com/rosaldo/api-mcp/internal/spec"
)

// version is overwritten at build time (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	// Logs go to stderr because in stdio mode stdout IS the protocol channel.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	log.SetPrefix("api-mcp: ")

	cfg := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

type config struct {
	graphqlDepth   int
	blobDir        string
	blobIn         string
	specURL        string
	kind           string
	baseURL        string
	endpoint       string
	headers        list
	specHeaders    list
	includePaths   string
	excludePaths   string
	excludeOps     string
	serverFills    string
	query          list
	includeMethods string
	excludeMethods string
	depth          int

	authKind    string
	bearer      string
	basic       string
	apiKey      string
	flowURL     string
	flowFields  list
	tokenPath   string
	tokenTTL    time.Duration
	signAlgo    string
	signPayload string
	signInto    string
	signAppID   string
	signSecret  string
	signEncode  string
	signStamp   string

	mode string
	addr string
	path string
	list bool
}

// list accumulates a repeated flag (`--header a=1 --header b=2`), which reads better than one
// comma-separated string — and does not break when a value contains a comma.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// registerFlags declares every flag on fs and returns the config they write into.
//
// Separate from parseFlags so that a test can hold the real FlagSet and ask it what exists —
// which is how the flag table in the README is kept honest. Reading the source text instead
// would only ever check that a string appears twice.
func registerFlags(fs *flag.FlagSet) *config {
	c := &config{}
	fs.StringVar(&c.specURL, "spec", "", "specification: path, file://, http(s):// or - (stdin)")
	fs.Var(&c.specHeaders, "spec-header", "header sent when FETCHING the specification over http(s), name=value (repeatable). For an API that keeps its own specification behind a key. Separate from --header, which travels with API calls: the two are not necessarily the same credential. env:NAME reads that variable")
	fs.StringVar(&c.kind, "type", "", "force the dialect: openapi | graphql | discovery (default is to detect)")
	fs.StringVar(&c.baseURL, "base-url", "", "OpenAPI: beats the spec's `servers`")
	fs.StringVar(&c.endpoint, "endpoint", "", "GraphQL: where queries go (required)")
	fs.Var(&c.headers, "header", "fixed header on every call, name=value (repeatable). env:NAME reads that variable")
	fs.StringVar(&c.includePaths, "include-paths", "", "OpenAPI: comma-separated regexes of paths to include")
	fs.StringVar(&c.excludePaths, "exclude-paths", "", "OpenAPI: regexes of paths to exclude")
	fs.Var(&c.query, "query", "fixed query parameter on every call, name=value (repeatable), expanded and set BEFORE signing. env:NAME reads that variable")
	fs.StringVar(&c.serverFills, "server-fills", "", "OpenAPI: comma-separated parameter names this server supplies (e.g. timestamp,signature) — they leave the schema the model sees")
	fs.StringVar(&c.excludeOps, "exclude-ops", "", "OpenAPI: regexes matched against `METHOD /path` — the filter the other four cannot express, since they decide by path OR by method, never by the pair")
	fs.StringVar(&c.includeMethods, "include-methods", "", "OpenAPI: methods to include (GET,POST)")
	fs.StringVar(&c.excludeMethods, "exclude-methods", "", "OpenAPI: methods to exclude")
	fs.IntVar(&c.depth, "depth", 0, "how deep to expand nested types: GraphQL selections, discovery $ref chains (default 2)")
	// The old name, from when GraphQL was the only dialect with nested types to bound. Kept
	// working because someone's command line has it; `--depth` wins when both are given.
	fs.IntVar(&c.graphqlDepth, "graphql-depth", 0, "deprecated alias for --depth")

	fs.StringVar(&c.authKind, "auth", "", "static authentication: bearer | basic | apikey")
	fs.StringVar(&c.bearer, "bearer", "", "token for --auth=bearer. env:NAME reads that variable, keeping the secret out of the process arguments")
	fs.StringVar(&c.basic, "basic", "", "user:password for --auth=basic")
	fs.StringVar(&c.apiKey, "api-key", "", "key for --auth=apikey, as where:name=value (where = header|query|cookie)")
	fs.StringVar(&c.flowURL, "auth-url", "", "dynamic authentication: endpoint that trades credentials for a token")
	fs.Var(&c.flowFields, "auth-field", "field sent to --auth-url, name=value (repeatable). env:NAME reads that variable")
	fs.StringVar(&c.tokenPath, "auth-token-path", "data.token", "where the token sits in the --auth-url response")
	fs.DurationVar(&c.tokenTTL, "auth-ttl", 2*time.Hour, "how long the --auth-url token is valid")
	fs.StringVar(&c.signAlgo, "sign", "", "per-request signature: sha256 | hmac-sha256. For APIs where each call is signed over its own content")
	fs.StringVar(&c.signPayload, "sign-payload", "", "template of the string to sign, e.g. '{app_id}{timestamp}{body}{secret}'")
	fs.StringVar(&c.signInto, "sign-into", "", "where the signature goes: header:Name=template or query:name=template, with {signature}")
	fs.StringVar(&c.signAppID, "sign-app-id", "", "app id for --sign. env:NAME reads that variable")
	fs.StringVar(&c.signSecret, "sign-secret", "", "secret for --sign. env:NAME reads that variable")
	fs.StringVar(&c.signEncode, "sign-encoding", "hex", "how the signature is encoded: hex | base64")
	fs.StringVar(&c.signStamp, "sign-timestamp", "unix", "what {timestamp} expands to: unix | iso8601-ms")

	fs.StringVar(&c.blobDir, "blob-dir", "", "write oversized base64 in responses to this directory and hand the model the path instead")
	fs.StringVar(&c.blobIn, "blob-in", "", "read `file:<path>` arguments from this directory, base64 them, and send the bytes")
	fs.StringVar(&c.mode, "mode", "stdio", "transport: stdio | sse | http")
	fs.StringVar(&c.addr, "addr", ":8080", "address for sse and http modes")
	fs.StringVar(&c.path, "path", "/mcp", "endpoint path in http mode")
	fs.BoolVar(&c.list, "list", false, "list the tools the spec yields and exit (no server)")
	return c
}

func parseFlags() config {
	c := registerFlags(flag.CommandLine)
	flag.Parse()
	return *c
}

// specFetchHeaders is everything sent while FETCHING the specification — and nothing else.
//
// It exists as its own function so a test can assert what leaves the process, rather than what
// the flags happen to hold. The distinction matters because the mistake this guards against is
// not a wrong flag: it is someone reusing --header here, which would send the API credential to
// whatever host the --spec URL names. The operator authorised that credential for the API, not
// for that host.
func specFetchHeaders(c config) (map[string]string, error) {
	h, err := pairsFromEnv(c.specHeaders)
	if err != nil {
		return nil, fmt.Errorf("--spec-header %w", err)
	}
	return h, nil
}

func run(ctx context.Context, c config) error {
	if c.specURL == "" {
		flag.Usage()
		return fmt.Errorf("missing --spec")
	}
	specHeaders, err := specFetchHeaders(c)
	if err != nil {
		return err
	}
	doc, err := spec.LoadWithHeaders(ctx, c.specURL, spec.Kind(c.kind), specHeaders)
	if err != nil {
		return err
	}
	applier, err := buildAuth(c)
	if err != nil {
		return err
	}

	headers, err := pairsFromEnv(c.headers)
	if err != nil {
		return fmt.Errorf("--header %w", err)
	}

	if c.depth == 0 {
		c.depth = c.graphqlDepth
	}

	var ops []core.Operation
	switch doc.Kind {
	case spec.KindDiscovery:
		ops, err = discovery.Operations(ctx, doc, discovery.Options{
			BaseURL:        c.baseURL,
			Auth:           applier,
			Headers:        headers,
			IncludePaths:   regexes(c.includePaths),
			ExcludePaths:   regexes(c.excludePaths),
			IncludeMethods: split(c.includeMethods),
			ExcludeMethods: split(c.excludeMethods),
			Depth:          c.depth,
		})
	case spec.KindGraphQL:
		ops, err = graphql.Operations(ctx, doc, graphql.Options{
			Endpoint: coalesce(c.endpoint, c.baseURL),
			Auth:     applier,
			Headers:  headers,
			Depth:    c.depth,
		})
	default:
		ops, err = openapi.Operations(ctx, doc, openapi.Options{
			BaseURL:        c.baseURL,
			Auth:           applier,
			Headers:        headers,
			IncludePaths:   regexes(c.includePaths),
			ExcludePaths:   regexes(c.excludePaths),
			ExcludeOps:     regexes(c.excludeOps),
			ServerFills:    conjunto(split(c.serverFills)),
			IncludeMethods: split(c.includeMethods),
			ExcludeMethods: split(c.excludeMethods),
		})
	}
	if err != nil {
		return err
	}

	if c.list {
		for _, op := range ops {
			fmt.Printf("%-40s %s\n", op.Name, op.Description)
		}
		return nil
	}

	log.Printf("%s: %d tools from %s", doc.Kind, len(ops), doc.Source)
	return mcpserver.Serve(ctx, ops, mcpserver.Config{
		Name: "api-mcp", Version: version,
		Instructions: doc.Instructions(),
		BlobDir:      c.blobDir,
		BlobIn:       c.blobIn,
		// Google declares every `format: byte` field of its APIs as base64URL; OpenAPI's is plain
		// base64. Deriving it from the dialect keeps the caller from having to know which is which.
		BlobInURLSafe: doc.Kind == spec.KindDiscovery,
		Mode:          mcpserver.Mode(c.mode), Addr: c.addr, Path: c.path,
	})
}

// fromEnv resolves a value that may point at an environment variable instead of holding the
// secret itself: `env:INVOLVE_SECRET` reads INVOLVE_SECRET.
//
// This exists because a process's arguments are public. Anything passed as `--bearer <token>`
// sits in /proc/<pid>/cmdline and in `ps` output, readable by every other process on the
// machine — including whatever the model decides to run. MCP clients declare secrets in the
// `env` block of their config for exactly this reason; this is how those reach the flags.
//
// A variable that is not set is an error, never an empty string: authenticating with "" fails
// later, somewhere that says nothing about a missing variable.
func fromEnv(value string) (string, error) {
	name, isEnv := strings.CutPrefix(value, "env:")
	if !isEnv {
		return value, nil
	}
	resolved, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return resolved, nil
}

// pairsFromEnv is `pairs` with every value resolved — for `--auth-field secret=env:SECRET`.
func pairsFromEnv(l list) (map[string]string, error) {
	m := map[string]string{}
	for _, item := range l {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		resolved, err := fromEnv(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		m[strings.TrimSpace(name)] = resolved
	}
	return m, nil
}

func buildAuth(c config) (auth.Applier, error) {
	chosen, err := chooseAuth(c)
	if err != nil {
		return nil, err
	}
	// `{uuid}` in a header is independent of how the call authenticates — an API can demand a
	// unique request id and no credential at all — so RequestID runs ahead of everything.
	return auth.Chain{auth.RequestID{}, chosen}, nil
}

func chooseAuth(c config) (auth.Applier, error) {
	// Per-request signing wins over everything: an API that signs each call has no fixed token
	// to fall back on, so configuring both means one of them is a leftover.
	if c.signAlgo != "" {
		appID, err := fromEnv(c.signAppID)
		if err != nil {
			return nil, err
		}
		secret, err := fromEnv(c.signSecret)
		if err != nil {
			return nil, err
		}
		if c.signPayload == "" || c.signInto == "" {
			return nil, fmt.Errorf("--sign requires --sign-payload and --sign-into")
		}
		queryFixa, err := pairsFromEnv(c.query)
		if err != nil {
			return nil, fmt.Errorf("--query %w", err)
		}
		return auth.Signature{
			Query:           queryFixa,
			Algo:            c.signAlgo,
			Payload:         c.signPayload,
			Into:            c.signInto,
			Encoding:        c.signEncode,
			TimestampFormat: c.signStamp,
			AppID:           appID,
			Secret:          secret,
		}, nil
	}
	// Dynamic wins over static: whoever configured a token flow did so precisely to avoid
	// depending on a fixed value.
	if c.flowURL != "" {
		fields, err := pairsFromEnv(c.flowFields)
		if err != nil {
			return nil, fmt.Errorf("--auth-field %w", err)
		}
		return &auth.Flow{
			URL:       c.flowURL,
			Fields:    fields,
			TokenPath: strings.Split(c.tokenPath, "."),
			TTL:       c.tokenTTL,
		}, nil
	}
	switch strings.ToLower(c.authKind) {
	case "":
		return auth.None{}, nil
	case "bearer":
		token, err := fromEnv(c.bearer)
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, fmt.Errorf("--auth=bearer requires --bearer")
		}
		return auth.Bearer{Token: token}, nil
	case "basic":
		creds, err := fromEnv(c.basic)
		if err != nil {
			return nil, err
		}
		user, password, ok := strings.Cut(creds, ":")
		if !ok {
			return nil, fmt.Errorf("--auth=basic requires --basic=user:password")
		}
		return auth.Basic{User: user, Password: password}, nil
	case "apikey":
		where, rest, ok := strings.Cut(c.apiKey, ":")
		if !ok {
			return nil, fmt.Errorf("--auth=apikey requires --api-key=where:name=value")
		}
		name, value, ok := strings.Cut(rest, "=")
		if !ok {
			return nil, fmt.Errorf("--auth=apikey requires --api-key=where:name=value")
		}
		resolved, err := fromEnv(value)
		if err != nil {
			return nil, err
		}
		return auth.APIKey{In: where, Name: name, Value: resolved}, nil
	default:
		return nil, fmt.Errorf("--auth=%q: use bearer, basic or apikey", c.authKind)
	}
}

func regexes(s string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range split(s) {
		re, err := regexp.Compile(p)
		if err != nil {
			log.Printf("ignoring invalid pattern %q: %v", p, err)
			continue
		}
		out = append(out, re)
	}
	return out
}

func split(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func coalesce(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// conjunto vira um mapa de consulta em minúsculas — nomes de parâmetro não são case-sensitive na
// prática, e exigir a grafia exata faria a flag falhar em silêncio.
func conjunto(nomes []string) map[string]bool {
	if len(nomes) == 0 {
		return nil
	}
	m := make(map[string]bool, len(nomes))
	for _, n := range nomes {
		m[strings.ToLower(strings.TrimSpace(n))] = true
	}
	return m
}
