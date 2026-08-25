// Package apertureapi provides read-only access to an Aperture endpoint.
package apertureapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

// Client is a read-only Aperture API client.
type Client struct {
	HTTPClient *http.Client
}

// NewClient returns a client with the same timeout historically used by the
// launcher's provider preflight.
func NewClient() *Client {
	return &Client{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

// Model describes one entry returned by GET /v1/models. Pricing values are
// kept as strings so callers do not lose the server's decimal precision.
type Model struct {
	ID                  string            `json:"id"`
	DisplayName         string            `json:"display_name"`
	PricingDisplayName  string            `json:"pricing_display_name,omitempty"`
	Object              string            `json:"object,omitempty"`
	Created             int64             `json:"created,omitempty"`
	SupportedEndpoints  []string          `json:"supported_endpoints"`
	ContextWindowTokens int64             `json:"context_window_tokens"`
	Metadata            ModelMetadata     `json:"metadata"`
	OwnedBy             string            `json:"owned_by,omitempty"`
	Pricing             map[string]string `json:"pricing,omitempty"`
	QualifiedID         string            `json:"qualified_id,omitempty"`
}

// ModelMetadata holds extended model metadata supplied by Aperture.
type ModelMetadata struct {
	Provider ModelProvider `json:"provider"`
}

// ModelProvider identifies the provider that owns a model.
type ModelProvider struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	RequiresClientAuth bool   `json:"requires_client_auth,omitempty"`
}

type modelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Providers fetches the compatibility-oriented provider catalog.
func (c *Client) Providers(ctx context.Context, host string) ([]config.ProviderInfo, error) {
	var providers []config.ProviderInfo
	if err := c.getJSON(ctx, host, "/api/providers", &providers); err != nil {
		return nil, err
	}
	return providers, nil
}

// Models fetches the rich OpenAI-compatible model catalog.
func (c *Client) Models(ctx context.Context, host string) ([]Model, error) {
	var list modelList
	if err := c.getJSON(ctx, host, "/v1/models", &list); err != nil {
		return nil, err
	}
	for i := range list.Data {
		providerID := list.Data[i].Metadata.Provider.ID
		if providerID != "" {
			list.Data[i].QualifiedID = providerID + "/" + list.Data[i].ID
		} else {
			list.Data[i].QualifiedID = list.Data[i].ID
		}
	}
	return list.Data, nil
}

func (c *Client) getJSON(ctx context.Context, host, path string, dst any) error {
	u := strings.TrimRight(host, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("creating request for %s: %w", u, err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, u)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("could not parse response from %s: %w", u, err)
	}
	return nil
}
