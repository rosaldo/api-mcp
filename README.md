# api-mcp

Point it at an API's specification and it becomes an MCP server. One tool per operation, no code
to write.

```sh
api-mcp --spec https://api.exemplo.com/openapi.yaml
```

It reads **OpenAPI 3.x**, **Swagger 2.0** and **GraphQL** — as JSON, YAML or SDL — from a file,
a URL or stdin. The dialect is detected from the content; `--type` forces it when detection gets
it wrong.

## Why it exists

Plenty of APIs have no MCP server, and the ones that do are not always auditable: using a
server hosted by a third party means handing it the credentials of the people you serve. With
the specification in hand the server needs neither to be written nor trusted to anyone — it is
generated, it runs wherever you put it, and the credentials never leave.

## Install

```sh
go install github.com/rosaldo/api-mcp@latest
```

## Usage

```sh
# see what the spec yields, without starting anything
api-mcp --spec ./openapi.yaml --list

# stdio (default) — this is how an MCP client starts the server
api-mcp --spec ./openapi.yaml --auth bearer --bearer "$TOKEN"

# GraphQL: a schema does not say where the API lives, so the endpoint is required
api-mcp --spec ./schema.graphql --endpoint https://api.example.com/graphql

# HTTP, if you would rather have a server running
api-mcp --spec ./openapi.yaml --mode http --addr :8080
```

In an MCP client:

```json
{
  "mcpServers": {
    "my-api": {
      "command": "api-mcp",
      "args": ["--spec", "https://api.example.com/openapi.yaml", "--auth", "bearer", "--bearer", "..."]
    }
  }
}
```

## Authentication

Static, when the token is fixed:

```sh
--auth bearer --bearer "$TOKEN"
--auth basic  --basic "user:password"
--auth apikey --api-key "header:X-Api-Key=$KEY"     # header | query | cookie
```

**Dynamic**, when the API trades credentials for a short-lived token — the case most tools do
not cover, and the one that makes a server work for two hours and then return nothing but 401:

```sh
api-mcp --spec ./openapi.yaml \
  --auth-url https://api.example.com/authenticate \
  --auth-field key="$KEY" --auth-field secret="$SECRET" \
  --auth-token-path data.token \
  --auth-ttl 2h
```

The token is fetched on the first call, kept in memory and renewed before it expires.

## GraphQL

Every `Query` and `Mutation` field becomes a tool. Since GraphQL requires the caller to say what
comes back, the **selection is assembled automatically**: the scalar fields of the return type,
descending two levels (`--graphql-depth` changes that). When the default does not fit, the tool
takes a `_select` argument with a hand-written selection.

Arguments travel as **GraphQL variables**, never interpolated into the query text.

The schema can be SDL or the JSON of an introspection query — useful when all you have is the
endpoint.

## Trimming the surface

A large spec becomes dozens of tools, and each one takes up the model's context:

```sh
--include-paths '^/v2/(offers|links)'   # regexes, comma-separated
--exclude-paths '^/admin'
--include-methods GET,POST
--exclude-methods DELETE
```

## All flags

| Flag | What |
|---|---|
| `--spec` | path, `file://`, `http(s)://` or `-` (stdin) |
| `--type` | `openapi` \| `graphql` — forces the dialect |
| `--base-url` | OpenAPI: beats the spec's `servers` |
| `--endpoint` | GraphQL: where queries go |
| `--header` | fixed header on every call, `name=value` (repeatable) |
| `--graphql-depth` | depth of the automatic selection (default 2) |
| `--mode` | `stdio` (default) \| `sse` \| `http` |
| `--addr`, `--path` | address and path in the network modes |
| `--list` | list the tools and exit |

## Releasing

```sh
./commit.sh feat "what changed"   # gate → version bump → CHANGELOG → tag
./push.sh                         # push, tag and GitHub Release
```

Both are shortcuts to `scripts/`.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — the design and the decisions behind it.

## Credits

The idea of serving a spec as MCP tools comes from
[swagger-mcp](https://github.com/danishjsheikh/swagger-mcp) (MIT), by Danish J Sheikh — the
one-tool-per-operation model, the filters and the three transports came from there. Thank you.

## License

MIT.
