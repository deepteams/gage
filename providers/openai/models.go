package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/deepteams/gage"
)

var _ gage.ModelLister = (*ChatClient)(nil)

// Models implements gage.ModelLister via GET {BaseURL}/models. Beyond the
// plain OpenAI list shape it reads the metadata extensions of the compatible
// servers built on this client: OpenRouter's name / context_length /
// top_provider.max_completion_tokens and vLLM's max_model_len.
func (c *ChatClient) Models(ctx context.Context) ([]gage.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: c.ProviderName, Status: resp.StatusCode, Body: string(b)}
	}

	var out struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			MaxModelLen   int    `json:"max_model_len"`
			TopProvider   struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: models: %w", c.ProviderName, err)
	}

	infos := make([]gage.ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		ctxWindow := m.ContextLength
		if ctxWindow == 0 {
			ctxWindow = m.MaxModelLen
		}
		infos = append(infos, gage.ModelInfo{
			ID:              m.ID,
			Name:            m.Name,
			ContextWindow:   ctxWindow,
			MaxOutputTokens: m.TopProvider.MaxCompletionTokens,
		})
	}
	return infos, nil
}
