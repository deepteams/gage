package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/deepteams/gage"
)

var _ gage.ModelLister = (*nativeProvider)(nil)

// Models implements gage.ModelLister via GET /api/tags, which lists the models
// pulled locally. /api/tags does not report context sizes, so only ID and Name
// are populated.
func (p *nativeProvider) Models(ctx context.Context) ([]gage.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.client().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, &gage.APIError{Provider: "ollama", Status: resp.StatusCode, Body: string(b)}
	}

	var out struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: models: %w", err)
	}

	infos := make([]gage.ModelInfo, 0, len(out.Models))
	for _, m := range out.Models {
		id := m.Model
		if id == "" {
			id = m.Name
		}
		infos = append(infos, gage.ModelInfo{ID: id, Name: m.Name})
	}
	return infos, nil
}
