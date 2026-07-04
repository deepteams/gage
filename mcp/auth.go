// Package mcp bridges Model Context Protocol servers into gage: it connects to
// a server over stdio or streamable HTTP, discovers its tools, and adapts each
// one to the gage.Tool port so it can be registered on an agent.
package mcp

import "net/http"

// headerTransport is an http.RoundTripper that injects static headers on every
// request. It backs bearer-token and custom-header authentication for HTTP MCP
// servers.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone to avoid mutating a shared request.
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		clone.Header.Set(k, v)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// httpClientWithHeaders returns an *http.Client that adds the given headers to
// every request. Use it for bearer tokens (Authorization: Bearer ...) or any
// static auth headers an HTTP MCP server requires.
func httpClientWithHeaders(base *http.Client, headers map[string]string) *http.Client {
	if len(headers) == 0 {
		if base != nil {
			return base
		}
		return http.DefaultClient
	}
	var underlying http.RoundTripper = http.DefaultTransport
	c := &http.Client{}
	if base != nil {
		*c = *base
		if base.Transport != nil {
			underlying = base.Transport
		}
	}
	c.Transport = &headerTransport{base: underlying, headers: headers}
	return c
}

// BearerHeaders builds an Authorization: Bearer header map.
func BearerHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}
