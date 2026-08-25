package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

type opencodeConfig struct {
	Schema     string                      `json:"$schema,omitempty"`
	Provider   map[string]opencodeProvider `json:"provider,omitempty"`
	Permission map[string]string           `json:"permission,omitempty"`
}

type opencodeProvider struct {
	NPM     string                        `json:"npm,omitempty"`
	Name    string                        `json:"name,omitempty"`
	Options map[string]string             `json:"options,omitempty"`
	Models  map[string]opencodeModelEntry `json:"models,omitempty"`
	// Whitelist limits the active model list to exactly these IDs. Without
	// it, OpenCode merges its built-in models.dev database entries on top of
	// our config (e.g. for provider IDs like "openai" or "anthropic").
	Whitelist []string `json:"whitelist,omitempty"`
}

type opencodeModelEntry struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// pickSDK chooses the AI SDK npm package and baseline options for a provider
// based on its compatibility map. Order matters: when a provider supports
// multiple protocols, the first match wins.
func pickSDK(compat map[string]bool, apertureHost string) (npm string, options map[string]string) {
	switch {
	case compat["openai_responses"]:
		return "@ai-sdk/openai", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["anthropic_messages"]:
		return "@ai-sdk/anthropic", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["openai_chat"]:
		return "@ai-sdk/openai-compatible", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["google_generate_content"] || compat["google_raw_predict"]:
		// Setting apiKey triggers the Vertex SDK's "express mode" which skips
		// google-auth-library / ADC. We still need the full project-scoped
		// path because aperture's vertex router only matches that pattern;
		// the magic _aperture_auto_*_ placeholders are rewritten upstream.
		return "@ai-sdk/google-vertex", map[string]string{
			"apiKey":  "not-required",
			"baseURL": apertureHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
		}
	case compat["bedrock_model_invoke"] || compat["bedrock_converse"]:
		return "@ai-sdk/amazon-bedrock", map[string]string{
			"region":   "us-east-1",
			"endpoint": apertureHost + "/bedrock",
		}
	case compat["gemini_generate_content"]:
		return "@ai-sdk/google", map[string]string{
			"baseURL": apertureHost + "/v1beta",
			"apiKey":  "not-required",
		}
	}
	return "", nil
}

// writeProviderConfig writes a unique per-launch OpenCode config and returns
// the path plus an idempotent cleanup
// function that removes the file. The config defines one provider (the
// chosen one) mapped to the SDK picked from its compatibility map.
func writeProviderConfig(apertureHost string, p config.ProviderInfo, yolo bool) (string, func() error, error) {
	npm, options := pickSDK(p.Compatibility, apertureHost)
	providerID := providerConfigID(p)

	models := make(map[string]opencodeModelEntry, len(p.Models))
	whitelist := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		fqn := providerID + "/" + m
		// Model keys and whitelist entries are relative to the provider.
		// OpenCode qualifies them as "provider/model" at the CLI boundary.
		models[m] = opencodeModelEntry{ID: m, Name: fqn}
		whitelist = append(whitelist, m)
	}

	cfg := opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		Provider: map[string]opencodeProvider{
			providerID: {
				NPM:       npm,
				Name:      "Aperture (" + p.ID + ")",
				Options:   options,
				Models:    models,
				Whitelist: whitelist,
			},
		},
	}
	if yolo {
		cfg.Permission = map[string]string{"*": "allow"}
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	configDir := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", nil, err
	}
	if err := cleanupStaleConfigs(configDir, time.Now()); err != nil {
		return "", nil, err
	}
	file, err := os.CreateTemp(configDir, "aperture-*.json")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", nil, err
	}
	var once sync.Once
	var cleanupErr error
	return path, func() error {
		once.Do(func() { cleanupErr = os.Remove(path) })
		return cleanupErr
	}, nil
}

func cleanupStaleConfigs(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		owned := name == "tmp_aperture_config.json" ||
			(strings.HasPrefix(name, "aperture-") && strings.HasSuffix(name, ".json"))
		if !owned || entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) < 24*time.Hour {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
