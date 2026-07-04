package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"sync"

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

	// syncMu serializes tool-list reconciliations (see WithToolSync).
	syncMu  sync.Mutex
	syncReg gage.ToolRegistry
}

// Option configures a Client at connect time.
type Option func(*clientOptions)

type clientOptions struct {
	syncReg  gage.ToolRegistry
	sampling gage.Provider
}

// WithToolSync keeps reg in sync with the server's tool list: when the server
// sends a tools/list_changed notification, the client re-lists the tools and
// reconciles the registry — new tools are registered, vanished tools are
// unregistered, and changed tools are replaced. Only tools carrying this
// client's "<server>__" prefix are touched. Reconciliation is serialized, so
// concurrent notifications cannot interleave.
//
// The option only reacts to change notifications; call Register (or SyncTools)
// once after connecting to perform the initial registration.
func WithToolSync(reg gage.ToolRegistry) Option {
	return func(o *clientOptions) { o.syncReg = reg }
}

// WithSamplingProvider declares the MCP sampling capability and serves the
// server's sampling/createMessage requests with p: the request's messages,
// system prompt and generation parameters are mapped onto a gage.Request,
// p.Stream is run to completion, and the accumulated text is returned as the
// assistant message. Requests carrying non-text content are rejected.
func WithSamplingProvider(p gage.Provider) Option {
	return func(o *clientOptions) { o.sampling = p }
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
func ConnectStdio(ctx context.Context, cfg StdioConfig, opts ...Option) (*Client, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp: stdio command is required")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if len(cfg.Env) > 0 {
		cmd.Env = append(cmd.Environ(), cfg.Env...)
	}
	transport := &mcpsdk.CommandTransport{Command: cmd}
	return connect(ctx, cfg.Name, transport, opts...)
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
func ConnectHTTP(ctx context.Context, cfg HTTPConfig, opts ...Option) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("mcp: http endpoint is required")
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   cfg.Endpoint,
		HTTPClient: httpClientWithHeaders(cfg.HTTPClient, cfg.Headers),
	}
	return connect(ctx, cfg.Name, transport, opts...)
}

func connect(ctx context.Context, name string, transport mcpsdk.Transport, opts ...Option) (*Client, error) {
	if name == "" {
		return nil, fmt.Errorf("mcp: server name is required")
	}
	var o clientOptions
	for _, opt := range opts {
		opt(&o)
	}
	c := &Client{name: name, syncReg: o.syncReg}

	// Notification handlers and the sampling capability must be wired before
	// the session exists, so the closures reach the session through the request
	// (or through c after connect returns).
	var sdkOpts *mcpsdk.ClientOptions
	if o.syncReg != nil || o.sampling != nil {
		sdkOpts = &mcpsdk.ClientOptions{}
		if o.syncReg != nil {
			sdkOpts.ToolListChangedHandler = func(ctx context.Context, req *mcpsdk.ToolListChangedRequest) {
				// Notification handlers cannot return errors; a failed refresh
				// leaves the registry as-is until the next notification.
				_ = c.syncTools(ctx, req.Session)
			}
		}
		if o.sampling != nil {
			sdkOpts.CreateMessageHandler = func(ctx context.Context, req *mcpsdk.CreateMessageRequest) (*mcpsdk.CreateMessageResult, error) {
				return createMessage(ctx, o.sampling, req.Params)
			}
		}
	}

	client := mcpsdk.NewClient(implementation, sdkOpts)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect %s: %w", name, err)
	}
	c.session = session
	return c, nil
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
// are prefixed with a provider-safe server name ("<server>__<tool>", with
// unsafe characters normalized) to avoid collisions in a shared registry.
// Pagination is followed to completion.
func (c *Client) Tools(ctx context.Context) ([]gage.Tool, error) {
	return listTools(ctx, c.session, c.name)
}

func listTools(ctx context.Context, session *mcpsdk.ClientSession, server string) ([]gage.Tool, error) {
	var out []gage.Tool
	var cursor string
	for {
		res, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: list tools %s: %w", server, err)
		}
		for _, t := range res.Tools {
			out = append(out, adaptTool(session, server, t))
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
