# api-mcp

Point it at an API's specification and it becomes an MCP server. One tool per operation, no code
to write.

```sh
api-mcp --spec https://api.exemplo.com/openapi.yaml
```

It reads **OpenAPI 3.x**, **Swagger 2.0**, **GraphQL** and Google's **Discovery Document** — as
JSON, YAML or SDL — from a file,
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

# A scheme that signs the verb too, base64-encoded, with an ISO 8601 timestamp
--sign hmac-sha256 \
--sign-payload '{timestamp}{method}{path}{body}' \
--sign-into 'header:X-SIGN={signature}' \
--sign-encoding base64 --sign-timestamp iso8601-ms \
--header 'X-TIMESTAMP={timestamp}' \
--sign-app-id env:API_KEY --sign-secret env:API_SECRET
```

Placeholders: `{app_id}` `{secret}` `{timestamp}` `{method}` (the verb, uppercase) `{body}`
`{path}` `{query}` (sorted, `k=v` joined) and `{signature}` in `--sign-into`.

Two shapes vary between APIs and neither announces itself when wrong — both fail as an
authentication error that says nothing about format:

| Flag | Values | Default |
|---|---|---|
| `--sign-encoding` | `hex`, `base64` | `hex` |
| `--sign-timestamp` | `unix` (seconds), `iso8601-ms` (`2020-12-08T09:08:57.715Z`) | `unix` |

`{timestamp}` expands to the **same instant** in the payload and in `--sign-into`, so a scheme
that signs the timestamp and also sends it in a header stays consistent. Signing one instant and
announcing another is a signature error that looks like a wrong secret.

## Google APIs

Google does not publish OpenAPI. They publish a **Discovery Document**, their own format, at a
predictable address — and they publish one for **over three hundred services**: Drive, Sheets,
Calendar, Gmail, YouTube, Search Console, Analytics, Business Profile, and the rest.

```sh
api-mcp --spec 'https://www.googleapis.com/discovery/v1/apis/sheets/v4/rest'
api-mcp --spec 'https://www.googleapis.com/discovery/v1/apis/calendar/v3/rest'
```

The address is `https://www.googleapis.com/discovery/v1/apis/{api}/{version}/rest`; a few services
serve their own, like `https://generativelanguage.googleapis.com/$discovery/rest?version=v1beta`.
The dialect is detected from the document, so there is nothing to declare.

**Trim them.** These are large surfaces — Drive is 64 methods, YouTube 83, Gmail 79 — and a
connector is usually for one job:

```sh
api-mcp --spec '…/drive/v3/rest' --include-paths '^/files' --exclude-methods DELETE
```

**Nested types are bounded** by `--depth` (2 by default). Google's types refer to each other
freely and some refer to themselves: expanded without a limit, a single Gemini
`models.generateContent` tool comes to about 60 KB of JSON Schema. At the default it is 4 KB, and
what lies past the limit is described as an object with a pointer to the API's own reference —
the model can still send it.

Two details this dialect handles that a generic reader would get wrong: `{+name}` is *reserved
expansion*, so a value like `models/veo/operations/abc` keeps its slashes instead of being
percent-encoded into a 404; and a `repeated` query parameter is sent repeated, not joined with
commas.

## GraphQL

Every `Query` and `Mutation` field becomes a tool. Since GraphQL requires the caller to say what
comes back, the **selection is assembled automatically**: the scalar fields of the return type,
descending two levels (`--graphql-depth` changes that). When the default does not fit, the tool
takes a `_select` argument with a hand-written selection.

Arguments travel as **GraphQL variables**, never interpolated into the query text.

The schema can be SDL or the JSON of an introspection query — useful when all you have is the
endpoint.

## What the server says about itself

On connect, the server passes the spec's own `info.title` and `info.description` to the client as
its instructions. This is not decoration. Clients that support tool search — the default in Claude
Code — load only tool **names** and these instructions when a session opens, and keep every
description and parameter schema deferred until the model goes looking. A server that says nothing
about itself is a server the model has no reason to search.

So the `description` in your spec is doing real work. Put the *what for* in its first sentence:

```yaml
info:
  title: cobalt
  description: >-
    Download video, audio and images from social platforms and video sites: youtube, tiktok,
    instagram, twitter, facebook, reddit, soundcloud, vimeo and others.
```

Claude Code truncates instructions at 2KB; anything longer is cut on a word boundary here, so
write the part that matters first. A schema with no `info` — GraphQL SDL, for one — simply sends
no instructions.

## Media that would not fit

Some APIs answer with the file itself, base64'd into the JSON. A single generated image comes back
as **1.17 MB** in one field, a music clip as **993 KB** — around 300,000 tokens for one call, of
bytes the model can neither look at nor save.

Point `--blob-dir` at a directory and those fields go to disk instead:

```sh
--blob-dir ./downloads
```

The response the model receives keeps its shape; only the oversized field is replaced:

```json
{"saved_to":"downloads/1af9eb0862f4fa94.png","bytes":877657,"mime_type":"image/png"}
```

Measured on a real response: **1,170,912 bytes in, 815 bytes out**, with the PNG written whole.

The file is named after its own digest, so generating the same thing twice writes one file rather
than two, and two different results never collide. The extension comes from the mime type declared
next to the bytes. Strings below 8 KB are left alone, prose is never touched (spaces are not in the
base64 alphabet), and a write that fails changes nothing — the model still gets its answer.

Without the flag, nothing here happens.

## Trimming the surface

A large spec becomes dozens of tools, and each one takes up the model's context **once the model
loads it** — with tool search, that happens on demand rather than at session start:

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
./push.sh                         # push the commits and the tag; CI builds and publishes
./push.sh --full                  # ...or build the binaries here and upload them
```

Both are shortcuts to `scripts/`. Pushing a `vX.Y.Z` tag starts the release workflow, which
cross-compiles and publishes; `--full` does the same locally, for when the workflow cannot run.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — the design and the decisions behind it.

## Credits

The idea of serving a spec as MCP tools comes from
[swagger-mcp](https://github.com/danishjsheikh/swagger-mcp) (MIT), by Danish J Sheikh — the
one-tool-per-operation model, the filters and the three transports came from there. Thank you.

## License

MIT.
