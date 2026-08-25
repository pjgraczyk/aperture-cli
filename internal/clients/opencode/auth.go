package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

const codexAuthPluginName = "opencode-openai-codex-auth"

type authSetupState struct {
	pluginInstalled bool
	authenticated   bool
}

func detectAuthSetup() (authSetupState, error) {
	plugin, err := detectPlugin()
	if err != nil {
		return authSetupState{}, err
	}
	auth, err := detectOpenAIAuth()
	if err != nil {
		return authSetupState{}, err
	}
	return authSetupState{pluginInstalled: plugin, authenticated: auth}, nil
}

func detectPlugin() (bool, error) {
	dir, err := opencodeConfigDir()
	if err != nil {
		return false, err
	}
	for _, name := range []string{"opencode.jsonc", "opencode.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read OpenCode config %s: %w", name, err)
		}
		standard, err := hujson.Standardize(data)
		if err != nil {
			return false, fmt.Errorf("parse OpenCode config %s: %w", name, err)
		}
		var cfg struct {
			Plugin []string `json:"plugin"`
		}
		if err := json.Unmarshal(standard, &cfg); err != nil {
			return false, fmt.Errorf("parse OpenCode config %s: %w", name, err)
		}
		for _, plugin := range cfg.Plugin {
			if plugin == codexAuthPluginName || strings.HasPrefix(plugin, codexAuthPluginName+"@") {
				return true, nil
			}
		}
	}
	return false, nil
}

func detectOpenAIAuth() (bool, error) {
	dir, err := opencodeDataDir()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read OpenCode auth store: %w", err)
	}
	var auth map[string]json.RawMessage
	if err := json.Unmarshal(data, &auth); err != nil {
		return false, fmt.Errorf("parse OpenCode auth store: %w", err)
	}
	value, ok := auth[codexAuthProvider]
	return ok && len(bytes.TrimSpace(value)) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null")), nil
}

func opencodeConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func opencodeDataDir() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "opencode"), nil
}
