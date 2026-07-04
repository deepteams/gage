package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Resource describes a resource exposed by an MCP server.
type Resource struct {
	// URI identifies the resource; pass it to ReadResource.
	URI string
	// Name is the resource's programmatic name.
	Name string
	// Title is an optional human-readable display name.
	Title string
	// Description says what the resource represents.
	Description string
	// MIMEType is the resource's MIME type, if known.
	MIMEType string
	// Size is the raw content size in bytes, if known.
	Size int64
}

// ResourceTemplate describes a parameterized resource (RFC 6570 URI template)
// exposed by an MCP server.
type ResourceTemplate struct {
	// URITemplate is the RFC 6570 template used to construct resource URIs.
	URITemplate string
	// Name is the template's programmatic name.
	Name string
	// Title is an optional human-readable display name.
	Title string
	// Description says what resources the template yields.
	Description string
	// MIMEType applies to all matching resources, if uniform.
	MIMEType string
}

// Resources lists the server's resources. Pagination is followed to completion.
func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	var cursor string
	for {
		res, err := c.session.ListResources(ctx, &mcpsdk.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: list resources %s: %w", c.name, err)
		}
		for _, r := range res.Resources {
			out = append(out, Resource{
				URI:         r.URI,
				Name:        r.Name,
				Title:       r.Title,
				Description: r.Description,
				MIMEType:    r.MIMEType,
				Size:        r.Size,
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// ResourceTemplates lists the server's resource templates. Pagination is
// followed to completion.
func (c *Client) ResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	var out []ResourceTemplate
	var cursor string
	for {
		res, err := c.session.ListResourceTemplates(ctx, &mcpsdk.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: list resource templates %s: %w", c.name, err)
		}
		for _, t := range res.ResourceTemplates {
			out = append(out, ResourceTemplate{
				URITemplate: t.URITemplate,
				Name:        t.Name,
				Title:       t.Title,
				Description: t.Description,
				MIMEType:    t.MIMEType,
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// ReadResource reads the resource at uri and maps its contents onto gage
// content parts: text contents become text parts, image blobs become image
// parts (base64 data + media type), and other binary blobs become a text part
// noting the MIME type and size.
func (c *Client) ReadResource(ctx context.Context, uri string) ([]gage.ContentPart, error) {
	res, err := c.session.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("mcp: read resource %s %s: %w", c.name, uri, err)
	}
	out := make([]gage.ContentPart, 0, len(res.Contents))
	for _, rc := range res.Contents {
		out = append(out, resourcePart(rc))
	}
	return out, nil
}

// resourcePart maps a single MCP resource contents onto a gage content part.
func resourcePart(rc *mcpsdk.ResourceContents) gage.ContentPart {
	if rc.Blob == nil {
		return gage.TextPart(rc.Text)
	}
	if strings.HasPrefix(rc.MIMEType, "image/") {
		return imagePart(rc.Blob, rc.MIMEType)
	}
	mime := rc.MIMEType
	if mime == "" {
		mime = "application/octet-stream"
	}
	return gage.TextPart(fmt.Sprintf("[binary resource %s: %s, %d bytes]", rc.URI, mime, len(rc.Blob)))
}
