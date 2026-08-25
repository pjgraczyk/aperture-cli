package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// writeConfig creates (or refreshes) the persistent CODEX_HOME directory
// holding auth.json and config.toml. Returns the directory path suitable
// for the CODEX_HOME environment variable.
//
// auth.json is pre-populated so Codex's first-run login prompt is skipped.
// config.toml pins the model provider to "aperture" pointing at the current
// aperture gateway.
//
// The path is the legacy "<config>/aperture/codex-home" used before the
// clients refactor, preserved so any per-home state Codex has stored under
// it continues to resolve.
func writeConfig(apertureHost string, subscription bool) (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	codexHome := filepath.Join(cfgDir, "aperture", "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return "", err
	}

	authPath := filepath.Join(codexHome, "auth.json")
	if subscription {
		// v0.0.8 wrote a fake API key on every launch. Remove that exact legacy
		// file when switching to subscription auth so Codex can prompt for a
		// ChatGPT login, but never overwrite a real OAuth credential.
		if err := removeLegacyPlaceholderAuth(authPath); err != nil {
			return "", err
		}
	} else {
		if err := ensurePlaceholderAuth(authPath); err != nil {
			return "", err
		}
	}

	apertureHost = strings.TrimRight(apertureHost, "/")
	baseURL := apertureHost + "/v1"
	authConfig := "env_key = \"OPENAI_API_KEY\"\n"
	if subscription {
		baseURL = apertureHost + "/codex"
		authConfig = "requires_openai_auth = true\n"
	}
	cfg := "model_provider = \"aperture\"\n\n" +
		"[model_providers.aperture]\n" +
		"name = \"Aperture\"\n" +
		"base_url = " + strconv.Quote(baseURL) + "\n" +
		"wire_api = \"responses\"\n" +
		authConfig +
		"supports_websockets = false\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(cfg), 0o600); err != nil {
		return "", err
	}

	return codexHome, nil
}

func ensurePlaceholderAuth(path string) error {
	if _, err := os.Stat(path); err == nil {
		// A real ChatGPT login can coexist with an env-key-backed custom
		// provider. Preserve it so switching providers never logs the user out.
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	auth := map[string]any{
		"auth_mode":      "apikey",
		"OPENAI_API_KEY": "not-needed",
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func removeLegacyPlaceholderAuth(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var auth map[string]any
	if json.Unmarshal(data, &auth) != nil {
		return nil
	}
	if len(auth) == 2 && auth["auth_mode"] == "apikey" && auth["OPENAI_API_KEY"] == "not-needed" {
		return os.Remove(path)
	}
	return nil
}
