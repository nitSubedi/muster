// Package ollama is a minimal client for the parts of the Ollama API Muster
// needs to build a node's model inventory.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nitSubedi/muster/internal/node"
)

// Client talks to one Ollama server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client with a short timeout suited to inventory polling.
func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

type tagsResponse struct {
	Models []json.RawMessage `json:"models"`
}

type psResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// Models lists installed models, marking those currently loaded in memory.
func (c *Client) Models(ctx context.Context) ([]node.Model, error) {
	var tags tagsResponse
	if err := c.get(ctx, "/api/tags", &tags); err != nil {
		return nil, err
	}
	var ps psResponse
	if err := c.get(ctx, "/api/ps", &ps); err != nil {
		return nil, err
	}
	loaded := make(map[string]bool, len(ps.Models))
	for _, m := range ps.Models {
		loaded[node.NormalizeModel(m.Name)] = true
	}
	out := make([]node.Model, 0, len(tags.Models))
	for _, raw := range tags.Models {
		var m struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Name == "" {
			continue
		}
		out = append(out, node.Model{
			Name:   m.Name,
			Size:   m.Size,
			Loaded: loaded[node.NormalizeModel(m.Name)],
			Tag:    raw,
		})
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
