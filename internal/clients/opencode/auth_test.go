package opencode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectAuthSetupJSONCAndXDG(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "opencode")
	dataDir := filepath.Join(root, "data", "opencode")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.jsonc"), []byte(`{
        // OpenCode permits JSONC.
        "plugin": ["opencode-openai-codex-auth@latest",],
      }`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "auth.json"), []byte(`{"openai":{"type":"oauth"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := detectAuthSetup()
	if err != nil {
		t.Fatal(err)
	}
	if !state.pluginInstalled || !state.authenticated {
		t.Fatalf("state = %+v", state)
	}
}

func TestDetectAuthSetupRejectsMalformedFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	dir := filepath.Join(root, "config", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := detectAuthSetup(); err == nil {
		t.Fatal("detectAuthSetup accepted malformed config")
	}
}

func TestDetectOpenAIAuthRequiresNonNullKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	dir := filepath.Join(root, "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := detectOpenAIAuth()
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("null auth was treated as authenticated")
	}
}
