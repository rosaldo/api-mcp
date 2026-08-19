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

Or download a binary for your platform from the
[releases](https://github.com/rosaldo/api-mcp/releases/latest) — Linux, macOS and Windows,
amd64 and arm64, with `SHA256SUMS` to verify them.

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
      "args": ["--spec", "https://api.example.com/openapi.yaml", "--auth", "bearer", "--bearer", "env:MY_API_TOKEN"],
      "env": { "MY_API_TOKEN": "..." }
    }
  }
}
```

## Authentication

**Keep secrets out of the arguments.** Any value can be read from an environment variable with
the `env:` prefix — a secret passed directly sits in `ps` output and in `/proc/<pid>/cmdline`,
where every other process on the machine can read it:

```sh
--bearer env:MY_API_TOKEN          # reads $MY_API_TOKEN
--auth-field secret=env:MY_SECRET
--header 'X-Key=env:MY_KEY'
```

An unset variable is an error, not an empty string.

Static, when the token is fixed:

```sh
--auth bearer --bearer env:TOKEN
--auth basic  --basic env:USER_AND_PASSWORD        # the variable holds user:password
--auth apikey --api-key header:X-Api-Key=env:KEY   # header | query | cookie
```

**Dynamic**, when the API trades credentials for a short-lived token — the case most tools do
not cover, and the one that makes a server work for two hours and then return nothing but 401:

```sh
api-mcp --spec ./openapi.yaml \
  --auth-url https://api.example.com/authenticate \
  --auth-field key=env:API_KEY --auth-field secret=env:API_SECRET \
  --auth-token-path data.token \
  --auth-ttl 2h
```

The token is fetched on the first call, kept in memory and renewed before it expires.

### Per-request signatures

Some APIs do not carry a token at all — they sign every call over its own content. Shopee's
affiliate API and TikTok Shop's are both like this, and no amount of bearer configuration
reaches them: the credential is not a value, it is a computation.

```sh
# Shopee: sha256 of appId+timestamp+body+secret, in an Authorization header
--sign sha256 \
--sign-payload '{app_id}{timestamp}{body}{secret}' \
--sign-into 'header:Authorization=SHA256 Credential={app_id}, Timestamp={timestamp}, Signature={signature}' \
--sign-app-id env:APP_ID --sign-secret env:APP_SECRET

# TikTok Shop: HMAC-SHA256 over path+sorted query+body, as a `sign` parameter
--sign hmac-sha256 \
--sign-payload '{path}{query}{body}' \
--sign-into 'query:sign={signature}' \
--sign-app-id env:APP_KEY --sign-secret env:APP_SECRET
```

Placeholders: `{app_id}` `{secret}` `{timestamp}` (unix seconds) `{body}` `{path}` `{query}`
(sorted, `k=v` joined) and `{signature}` in `--sign-into`.

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
./push.sh                         # build the binaries, push, tag and publish the Release
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
