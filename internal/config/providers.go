package config

// ProviderInfo mirrors the JSON response from GET /api/providers.
type ProviderInfo struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Models        []string        `json:"models"`
	Compatibility map[string]bool `json:"compatibility"`
	// RequiresClientAuth is set for passthrough-only providers, where the
	// selected client must supply its own API key or subscription OAuth token.
	RequiresClientAuth bool `json:"requires_client_auth,omitempty"`
}

// DisplayName returns the provider's Name, falling back to ID if Name is empty.
func (p ProviderInfo) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}
