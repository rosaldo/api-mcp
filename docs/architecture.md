# Architecture

The whole project turns on one type:

```go
type Operation struct {
    Name, Description string
    Input  Schema                                  // what the model fills in
    Invoke func(ctx, args) (string, error)         // what happens when it calls
}
```

An `Operation` is deliberately poor: nothing in it says whether underneath there is a `GET` with
a query string, a `POST` with a form, or a GraphQL query. That poverty is the point.

```
spec/       WHERE it comes from (file, URL, stdin) and WHAT it is (OpenAPI, Swagger, GraphQL)
              ↓ Document
dialect/    translates into operations — openapi/ and graphql/, one per dialect
              ↓ []core.Operation
mcpserver/  publishes them as MCP tools · stdio | sse | http
```

The server knows no dialect, and the dialect knows no protocol. A new dialect (gRPC-Web, SOAP,
whatever) arrives as a translator — nothing else is touched.

## Describing ≠ executing

The separation that holds everything up: the dialect **describes** the operation (name,
description, schema) and returns an `Invoke` that **executes** it. Both are born together, in
the same place, but they travel apart — whoever registers the tool never needs to know what is
on the other side.

Without that boundary, every new dialect becomes a branch inside the function that registers
tools — and REST and GraphQL are different enough (one returns a document, the other demands
you say what you want back) for that branch to contaminate both.

## Decisions worth explaining

**The spec is read by a library, not by a parser of our own.** `kin-openapi` resolves `$ref`
(nested and external included), reads JSON and YAML, and understands 3.0 and 3.1; Swagger 2.0
comes in converted to 3 by the library itself. Writing that parser by hand costs a partial
model — and what the model misses reaches the language model impoverished.

**The schema goes to the model as it is in the spec.** Enum, format, minimum, maximum, array
items, nested objects. Every field dropped along the way is a call the model has to guess at.

**The dialect is detected from content, not from the extension.** Extensions lie: a spec served
from a URL with no extension at all, a `.json` that is really YAML, a `.txt` holding OpenAPI.

**In GraphQL, we assemble the selection.** GraphQL requires the caller to say what comes back —
there is no "call it and see". Exposing one `graphql(query)` tool and letting the model write
the whole query hands it work the schema already answers. Here the selection comes from the
scalar fields of the return type, descending two levels; `_select` is there for when the default
does not fit.

**GraphQL arguments travel as variables.** Interpolating a value into the query text is
injection — a value with quotes rewrites the query — and it also loses the typing the server
uses to validate.

**An API error comes back as a result, not as a protocol failure.** The model needs to READ what
the API complained about in order to fix the call. A protocol error would tear down the
conversation without saying why.

**In `stdio`, stdout is the protocol channel.** Every log goes to stderr, no exceptions: one
stray print corrupts the conversation with the client.

## Authentication

Static (bearer, basic, api key) covers the simple case. What most tools do not cover is the
**dynamic** one: APIs that trade credentials for a short-lived token. Without it, an MCP server
for those APIs works until the token expires and then returns 401 until somebody restarts it.

`Flow` keeps the token in memory and renews it **before** expiry, with a margin of 10% of the
TTL (30s minimum) — renewing at expiry is a race against the server's clock, which is never
ours. An authentication error never carries the response body with it: whoever answers a token
request tends to echo back what it received, and what it received is the credential.
