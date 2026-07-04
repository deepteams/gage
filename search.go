package gage

import "context"

// SearchResult is a single web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchProvider is the port behind the web_search tool. Implementations live
// in the search sub-packages (duckduckgo, brave, tavily) or are supplied by the
// consumer.
type SearchProvider interface {
	// Search returns up to limit results for the query. A limit <= 0 means the
	// implementation's default.
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}
