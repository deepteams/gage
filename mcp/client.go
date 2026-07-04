package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientName identifies gage to MCP servers.
var implementation = &mcpsdk.Implementation{Name: "gage", Version: "0.1.0"}

// Client is a connected MCP session that exposes its server's tools as
// gage.Tools.
type Client struct {
	session *mcpsdk.ClientSession
	name    string
}

// StdioConfig connects to an MCP server launched as a subprocess over stdio.
type StdioConfig struct {
	// Name labels the server (used to prefix tool names). Required.
	Name string
	// Command is the executable to run.
	Command string
	// Args are the command arguments.
	Args []string
	// Env are extra environment variables ("KEY=VALUE"); appended to os.Environ.
	Env []string
	// Dir is the working directory (optional).
	Dir string
}

// ConnectStdio launches the command and connects over stdin/stdout.
func ConnectStdio(ctx context.Context, cfg StdioConfig) (*Client, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp: stdio command is required")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if len(cfg.Env) > 0 {
		cmd.Env = append(cmd.Environ(), cfg.Env...)
	}
	transport := &mcpsdk.CommandTransport{Command: cmd}
	return connect(ctx, cfg.Name, transport)
}

// HTTPConfig connects to an MCP server over streamable HTTP.
type HTTPConfig struct {
	// Name labels the server. Required.
	Name string
	// Endpoint is the server URL.
	Endpoint string
	// Headers are added to every request (bearer tokens, API keys, ...).
	Headers map[string]string
	// HTTPClient overrides the base client (headers are layered on top).
	HTTPClient *http.Client
}

// ConnectHTTP connects to a streamable HTTP MCP server.
func ConnectHTTP(ctx context.Context, cfg HTTPConfig) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("mcp: http endpoint is required")
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   cfg.Endpoint,
		HTTPClient: httpClientWithHeaders(cfg.HTTPClient, cfg.Headers),
	}
	return connect(ctx, cfg.Name, transport)
}

func connect(ctx context.Context, name string, transport mcpsdk.Transport) (*Client, error) {
	if name == "" {
		return nil, fmt.Errorf("mcp: server name is required")
	}
	client := mcpsdk.NewClient(implementation, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect %s: %w", name, err)
	}
	return &Client{session: session, name: name}, nil
}

// Close terminates the session (and, for stdio, the subprocess).
func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

// Name returns the server label.
func (c *Client) Name() string { return c.name }

// Tools discovers the server's tools and adapts them to gage.Tool. Tool names
// are prefixed with the server name ("<server>__<tool>") to avoid collisions in
// a shared registry. Pagination is followed to completion.
func (c *Client) Tools(ctx context.Context) ([]gage.Tool, error) {
	var out []gage.Tool
	var cursor string
	for {
		res, err := c.session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: list tools %s: %w", c.name, err)
		}
		for _, t := range res.Tools {
			out = append(out, c.adapt(t))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// Register discovers the server's tools and registers them on reg.
func (c *Client) Register(ctx context.Context, reg gage.ToolRegistry) error {
	tools, err := c.Tools(ctx)
	if err != nil {
		return err
	}
	for _, t := range tools {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}
