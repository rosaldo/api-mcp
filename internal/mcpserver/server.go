// Package mcpserver publishes operations as MCP tools.
//
// It is the only part that knows the protocol — and it knows no dialect at all. It takes
// core.Operation and registers it; if a gRPC or SOAP dialect shows up tomorrow, nothing here
// changes.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rosaldo/api-mcp/internal/core"
)

// Mode is how the server talks to the client.
type Mode string

const (
	ModeStdio Mode = "stdio" // the default: the MCP client starts the process and talks over pipes
	ModeSSE   Mode = "sse"
	ModeHTTP  Mode = "http"
)

// Config holds the transport decisions.
type Config struct {
	Name    string
	Version string
	Mode    Mode
	Addr    string
	Path    string // http mode only
}

// Serve registers the operations and answers until the context ends.
func Serve(ctx context.Context, ops []core.Operation, cfg Config) error {
	s := server.NewMCPServer(cfg.Name, cfg.Version, server.WithToolCapabilities(true))

	for _, op := range ops {
		s.AddTool(asTool(op), handler(op))
	}

	switch cfg.Mode {
	case ModeSSE:
		return server.NewSSEServer(s).Start(cfg.Addr)
	case ModeHTTP:
		return server.NewStreamableHTTPServer(s, server.WithEndpointPath(cfg.Path)).Start(cfg.Addr)
	default:
		// stdio: stdout IS the protocol channel. Any stray print there corrupts the
		// conversation — which is why every log in this process goes to stderr, no exceptions.
		return server.ServeStdio(s)
	}
}

// asTool converts the operation's description into the shape MCP publishes.
func asTool(op core.Operation) mcp.Tool {
	input := map[string]any{
		"type":       op.Input.Type,
		"properties": op.Input.Properties,
	}
	if len(op.Input.Required) > 0 {
		input["required"] = op.Input.Required
	}
	raw, err := json.Marshal(input)
	if err != nil {
		// A schema that will not serialise is a translation bug, not a user error: the tool is
		// still published (without it the API would be unreachable), just with no declared
		// arguments.
		log.Printf("api-mcp: schema for %q failed to serialise: %v", op.Name, err)
		raw = []byte(`{"type":"object"}`)
	}
	return mcp.NewToolWithRawSchema(op.Name, op.Description, raw)
}

func handler(op core.Operation) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, ok := req.Params.Arguments.(map[string]any)
		if !ok && req.Params.Arguments != nil {
			return mcp.NewToolResultError(fmt.Sprintf("arguments for %s did not arrive as an object", op.Name)), nil
		}
		if args == nil {
			args = map[string]any{}
		}
		out, err := op.Invoke(ctx, args)
		if err != nil {
			// An API error comes back as an error RESULT, not a protocol error: the model needs
			// to READ what the API complained about in order to fix the call. A protocol error
			// would tear down the conversation without saying why.
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	}
}
