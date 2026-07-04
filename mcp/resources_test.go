package mcp

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/deepteams/gage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResources(t *testing.T) {
	ctx := context.Background()
	png := []byte{0x89, 'P', 'N', 'G', 4, 5, 6}
	zip := []byte{'P', 'K', 3, 4, 0, 0}

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "docs", Version: "1.0"}, nil)
	server.AddResource(
		&mcpsdk.Resource{URI: "file:///readme.txt", Name: "readme", Description: "the readme", MIMEType: "text/plain", Size: 5},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{
				{URI: req.Params.URI, MIMEType: "text/plain", Text: "hello"},
			}}, nil
		})
	server.AddResource(
		&mcpsdk.Resource{URI: "file:///logo.png", Name: "logo", MIMEType: "image/png"},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{
				{URI: req.Params.URI, MIMEType: "image/png", Blob: png},
			}}, nil
		})
	server.AddResource(
		&mcpsdk.Resource{URI: "file:///bundle.zip", Name: "bundle", MIMEType: "application/zip"},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{
				{URI: req.Params.URI, MIMEType: "application/zip", Blob: zip},
			}}, nil
		})
	server.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{URITemplate: "file:///{path}", Name: "files", Description: "any file"},
		func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{
				{URI: req.Params.URI, MIMEType: "text/plain", Text: "templated"},
			}}, nil
		})

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	c, err := connect(ctx, "docs", clientT)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Listing.
	resources, err := c.Resources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 3 {
		t.Fatalf("expected 3 resources, got %d: %+v", len(resources), resources)
	}
	byURI := map[string]Resource{}
	for _, r := range resources {
		byURI[r.URI] = r
	}
	readme, ok := byURI["file:///readme.txt"]
	if !ok || readme.Name != "readme" || readme.Description != "the readme" || readme.MIMEType != "text/plain" || readme.Size != 5 {
		t.Fatalf("readme = %+v", readme)
	}

	// Text read.
	parts, err := c.ReadResource(ctx, "file:///readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Kind != gage.PartText || parts[0].Text != "hello" {
		t.Fatalf("text parts = %+v", parts)
	}

	// Image blob read.
	parts, err = c.ReadResource(ctx, "file:///logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Kind != gage.PartImage || parts[0].Image == nil {
		t.Fatalf("image parts = %+v", parts)
	}
	if parts[0].Image.MediaType != "image/png" || parts[0].Image.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("image source = %+v", parts[0].Image)
	}

	// Non-image blob read: a text note with MIME type and size.
	parts, err = c.ReadResource(ctx, "file:///bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Kind != gage.PartText {
		t.Fatalf("blob parts = %+v", parts)
	}
	if !strings.Contains(parts[0].Text, "application/zip") || !strings.Contains(parts[0].Text, "6 bytes") {
		t.Fatalf("blob note = %q", parts[0].Text)
	}

	// Templates.
	templates, err := c.ResourceTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 || templates[0].URITemplate != "file:///{path}" || templates[0].Name != "files" {
		t.Fatalf("templates = %+v", templates)
	}
}
