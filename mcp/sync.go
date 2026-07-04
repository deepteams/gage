package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SyncTools re-lists the server's tools and reconciles the registry passed to
// WithToolSync: tools that disappeared from the server are unregistered, new
// ones are registered, and changed ones (different description or schema) are
// replaced. Only tools carrying this client's normalized "<server>__" prefix
// are touched. It is the manual counterpart of the tools/list_changed
// notification handler and is safe to call concurrently with it.
func (c *Client) SyncTools(ctx context.Context) error {
	if c.syncReg == nil {
		return fmt.Errorf("mcp: sync tools %s: no registry (use WithToolSync)", c.name)
	}
	return c.syncTools(ctx, c.session)
}

// syncTools reconciles c.syncReg against the server's current tool list. The
// session is passed explicitly because the notification handler may fire
// before connect() has stored it on the Client.
func (c *Client) syncTools(ctx context.Context, session *mcpsdk.ClientSession) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()

	tools, err := listTools(ctx, session, c.name)
	if err != nil {
		return err
	}
	desired := make(map[string]gage.Tool, len(tools))
	for _, t := range tools {
		desired[t.Name()] = t
	}

	// Drop or mark-for-replacement everything of ours that is currently
	// registered; leave other servers' tools alone.
	prefix := exposedToolPrefix(c.name)
	for _, existing := range c.syncReg.List() {
		name := existing.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		want, ok := desired[name]
		if !ok {
			c.syncReg.Unregister(name) // vanished server-side
			continue
		}
		if sameTool(existing, want) {
			delete(desired, name) // unchanged: keep the registered instance
			continue
		}
		c.syncReg.Unregister(name) // changed: replace below
	}

	var errs []error
	for _, t := range desired {
		if err := c.syncReg.Register(t); err != nil {
			errs = append(errs, fmt.Errorf("mcp: sync tools %s: register %s: %w", c.name, t.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// sameTool reports whether a registered tool still matches its freshly listed
// counterpart (same description and input schema).
func sameTool(a, b gage.Tool) bool {
	return a.Description() == b.Description() && bytes.Equal(a.Schema(), b.Schema())
}
