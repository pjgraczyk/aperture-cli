package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

const testHost = "http://ai.example.com"

func TestInstallCommandDetectsPipelineFailures(t *testing.T) {
	plan := (&Client{}).Install(&config.Global{})
	cmd, err := plan.Run()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cmd.Args, []string{
		"bash", "-o", "pipefail", "-c",
		"curl -fsSL https://opencode.ai/install | bash",
	}) {
		t.Errorf("install command args = %q, want bash with pipefail", cmd.Args)
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "anthropic", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
		{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
		{ID: "bedrock", SupportedEndpoints: map[string]bool{config.EndpointBedrockConverse: true}},
		{ID: "openai-sub", RequiresClientAuth: true, SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}},
		{ID: "none", SupportedEndpoints: map[string]bool{"/unknown": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 4 {
		t.Errorf("compatibleProviders len = %d, want 4: %+v", len(got), got)
	}
}

func TestProviderAuthStep(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	// providerAuthStep requires npx to offer the plugin install menu.
	// Stub it so the test is hermetic on machines without Node.js.
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "npx"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider := config.ProviderInfo{
		ID:                 "openai-sub",
		Name:               "OpenAI (Subscription)",
		Models:             []string{"gpt-5.5"},
		RequiresClientAuth: true,
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}
	result := (&Client{}).providerAuthStep(&config.Global{}, provider)
	if result.Next == nil {
		t.Fatal("providerAuthStep did not show setup menu")
	}
	if len(result.Next.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(result.Next.Items))
	}
	for i, want := range []string{
		"Install the ChatGPT auth plugin",
		"Cancel",
	} {
		if got := result.Next.Items[i].Label; got != want {
			t.Errorf("item %d = %q, want %q", i, got, want)
		}
	}
	if got := fqnModels(provider); len(got) != 1 || got[0] != "openai/gpt-5.5" {
		t.Fatalf("fqnModels = %v, want [openai/gpt-5.5]", got)
	}
}

func TestModelStep(t *testing.T) {
	provider := config.ProviderInfo{
		ID:     "openrouter",
		Name:   "OpenRouter",
		Models: []string{"anthropic/claude-sonnet", "openai/gpt-5"},
		SupportedEndpoints: map[string]bool{
			config.EndpointOpenAIChat: true,
		},
	}
	g := &config.Global{}
	result := (&Client{}).modelStep(g, provider)
	if result.Next == nil {
		t.Fatal("modelStep launched directly; want model menu")
	}
	if got, want := result.Next.Title, "Choose a default model for OpenCode via OpenRouter:"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	wantLabels := []string{
		"openrouter/anthropic/claude-sonnet",
		"openrouter/openai/gpt-5",
	}
	if len(result.Next.Items) != len(wantLabels) {
		t.Fatalf("items = %d, want %d", len(result.Next.Items), len(wantLabels))
	}
	for i, want := range wantLabels {
		if got := result.Next.Items[i].Label; got != want {
			t.Errorf("item %d = %q, want %q", i, got, want)
		}
	}
}

func TestArgsForModel(t *testing.T) {
	if got := argsForModel(""); got != nil {
		t.Fatalf("argsForModel(empty) = %v, want nil", got)
	}
	model := "openrouter/anthropic/claude-sonnet"
	got := argsForModel(model)
	if len(got) != 2 || got[0] != "--model" || got[1] != model {
		t.Fatalf("argsForModel(%q) = %v", model, got)
	}
}

func TestModelOutputContainsExactModel(t *testing.T) {
	output := []byte("openai/gpt-5.5-mini\nopenai/gpt-5.5\n")
	if !modelOutputContains(output, "openai/gpt-5.5") {
		t.Fatal("exact model was not found")
	}
	if modelOutputContains(output, "openai/gpt-5") {
		t.Fatal("partial model match was accepted")
	}
}

func TestDefaultResolveModelsUsesNoPositionalArg(t *testing.T) {
	// opencode v2 rejects `models <provider>`; the preflight must not pass
	// a positional arg.
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "opencode")
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$*\" > \"$OPENCODE_TEST_ARGS\"\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_TEST_ARGS", argsFile)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := defaultResolveModels(ctx, fakeBin, nil, "some-provider", "some-provider/some-model"); err != nil {
		t.Fatalf("defaultResolveModels: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(raw)), "models"; got != want {
		t.Fatalf("invoked args = %q, want %q", got, want)
	}
}

func TestDefaultResolveModelsPropagatesCLIFailure(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := defaultResolveModels(ctx, fakeBin, nil, "p", "p/m")
	if err == nil || !strings.Contains(err.Error(), "model check failed") {
		t.Fatalf("err = %v, want model check failure", err)
	}
}

func TestLaunchRejectsUnknownModelLocally(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := &config.Global{ApertureHost: testHost}
	p := config.ProviderInfo{ID: "openrouter", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}}
	msg := (&Client{}).launch(g, p, "openrouter/nope").Cmd()
	done, ok := msg.(menu.SimpleDoneMsg)
	if !ok || done.Err == nil || !strings.Contains(done.Err.Error(), "not available") {
		t.Fatalf("launch result = %#v, want local not-available error", msg)
	}
	if g.LastLaunch.LastClientName != "" {
		t.Fatalf("rejected model recorded state: %+v", g.LastLaunch)
	}
	files, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".opencode", "aperture-*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary configs after rejection = %v, err=%v", files, err)
	}
}

func TestLaunchDoesNotRecordFailedPreflight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalResolve := resolveModels
	resolveModels = func(_ context.Context, _ string, _ map[string]string, _, _ string) error {
		return errString("invalid model")
	}
	t.Cleanup(func() { resolveModels = originalResolve })
	g := &config.Global{ApertureHost: testHost}
	p := config.ProviderInfo{ID: "openrouter", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}}
	msg := (&Client{}).launch(g, p, "openrouter/gpt-5").Cmd()
	continued, ok := msg.(menu.ContinueMsg)
	if !ok || continued.Err == nil {
		t.Fatalf("preflight result = %#v", msg)
	}
	if g.LastLaunch.LastClientName != "" {
		t.Fatalf("failed preflight recorded state: %+v", g.LastLaunch)
	}
	files, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".opencode", "aperture-*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary configs after failure = %v, err=%v", files, err)
	}
}

func TestLaunchRecordsSelectedModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalResolve := resolveModels
	resolveModels = func(_ context.Context, _ string, _ map[string]string, _, _ string) error { return nil }
	t.Cleanup(func() { resolveModels = originalResolve })
	provider := config.ProviderInfo{
		ID:                 "openrouter",
		Models:             []string{"anthropic/claude-sonnet"},
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true},
	}
	model := "openrouter/anthropic/claude-sonnet"
	g := &config.Global{ApertureHost: testHost}
	result := (&Client{}).launch(g, provider, model)
	if result.Cmd == nil {
		t.Fatal("launch returned nil command")
	}
	if g.LastLaunch.LastClientName != "" {
		t.Fatalf("launch state recorded before preflight: %+v", g.LastLaunch)
	}
	msg := result.Cmd()
	continued, ok := msg.(menu.ContinueMsg)
	if !ok || continued.Err != nil || continued.Result.Cmd == nil {
		t.Fatalf("preflight result = %#v", msg)
	}
	if g.LastLaunch.LastClientName != name || g.LastLaunch.LastProviderID != provider.ID || g.LastLaunch.LastModel != model {
		t.Fatalf("last launch = %+v", g.LastLaunch)
	}
}

func TestQuickSelectLabelIncludesModel(t *testing.T) {
	g := &config.Global{
		Providers: []config.ProviderInfo{{ID: "openrouter", Name: "OpenRouter"}},
		LastLaunch: config.LaunchState{
			LastProviderID: "openrouter",
			LastModel:      "openrouter/anthropic/claude-sonnet",
		},
	}
	want := "OpenCode via OpenRouter - openrouter/anthropic/claude-sonnet"
	if got := (&Client{}).QuickSelectLabel(g); got != want {
		t.Fatalf("QuickSelectLabel = %q, want %q", got, want)
	}
}

func TestPickSDK(t *testing.T) {
	cases := []struct {
		name      string
		endpoints map[string]bool
		wantNPM   string
	}{
		{"responses", map[string]bool{config.EndpointOpenAIResponses: true}, "@ai-sdk/openai"},
		{"anthropic", map[string]bool{config.EndpointAnthropicMessages: true}, "@ai-sdk/anthropic"},
		{"chat_only", map[string]bool{config.EndpointOpenAIChat: true}, "@ai-sdk/openai-compatible"},
		{"vertex", map[string]bool{config.EndpointVertexGemini: true}, "@ai-sdk/google-vertex"},
		{"bedrock", map[string]bool{config.EndpointBedrockConverse: true}, "@ai-sdk/amazon-bedrock"},
		{"gemini", map[string]bool{config.EndpointGemini: true}, "@ai-sdk/google"},
		{"none", map[string]bool{"/unknown": true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			npm, _ := pickSDK(config.ProviderInfo{SupportedEndpoints: tc.endpoints}, testHost)
			if npm != tc.wantNPM {
				t.Errorf("npm = %q, want %q", npm, tc.wantNPM)
			}
		})
	}
}

func TestPickSDK_ResponsesBeatsChat(t *testing.T) {
	npm, _ := pickSDK(config.ProviderInfo{SupportedEndpoints: map[string]bool{
		config.EndpointOpenAIChat:      true,
		config.EndpointOpenAIResponses: true,
	}}, testHost)
	if npm != "@ai-sdk/openai" {
		t.Errorf("npm = %q, want @ai-sdk/openai (responses should win)", npm)
	}
}

func TestWriteProviderConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	tests := []struct {
		name           string
		provider       config.ProviderInfo
		yolo           bool
		wantNPM        string
		wantOptions    map[string]string
		wantPermission map[string]string
	}{
		{
			name: "anthropic",
			provider: config.ProviderInfo{
				ID: "anthropic", Name: "Anthropic",
				Models:             []string{"claude-sonnet-4-5", "claude-haiku-4-5"},
				SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true},
			},
			wantNPM: "@ai-sdk/anthropic",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "bedrock",
			provider: config.ProviderInfo{
				ID: "bedrock", Name: "AWS Bedrock",
				Models:             []string{"us.anthropic.claude-opus-4-7"},
				SupportedEndpoints: map[string]bool{config.EndpointBedrockConverse: true},
			},
			wantNPM: "@ai-sdk/amazon-bedrock",
			wantOptions: map[string]string{
				"region":   "us-east-1",
				"endpoint": testHost + "/bedrock",
			},
		},
		{
			name: "vertex",
			provider: config.ProviderInfo{
				ID: "vertex", Name: "Vertex",
				Models: []string{"gemini-2.5-pro"},
				SupportedEndpoints: map[string]bool{
					config.EndpointVertexGemini: true,
					config.EndpointVertexClaude: true,
				},
			},
			wantNPM: "@ai-sdk/google-vertex",
			wantOptions: map[string]string{
				"apiKey":  "not-required",
				"baseURL": testHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
			},
		},
		{
			name: "openai",
			provider: config.ProviderInfo{
				ID: "openai", Name: "OpenAI",
				Models: []string{"gpt-5"},
				SupportedEndpoints: map[string]bool{
					config.EndpointOpenAIChat:      true,
					config.EndpointOpenAIResponses: true,
				},
			},
			wantNPM: "@ai-sdk/openai",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "openai_chat_only",
			provider: config.ProviderInfo{
				ID: "openrouter", Name: "OpenRouter",
				Models:             []string{"qwen/qwen3-235b-a22b-2507"},
				SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true},
			},
			wantNPM: "@ai-sdk/openai-compatible",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "yolo_permissions",
			provider: config.ProviderInfo{
				ID: "openrouter", Name: "OpenRouter",
				Models:             []string{"qwen/qwen3-235b-a22b-2507"},
				SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true},
			},
			yolo:    true,
			wantNPM: "@ai-sdk/openai-compatible",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
			wantPermission: map[string]string{"*": "allow"},
		},
		{
			name: "chatgpt_subscription",
			provider: config.ProviderInfo{
				ID:                 "openai-sub",
				Name:               "OpenAI (Subscription)",
				Models:             []string{"gpt-5.5"},
				RequiresClientAuth: true,
				SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
			},
			wantNPM: "@ai-sdk/openai",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath, cleanup, err := writeProviderConfig(testHost, tt.provider, tt.yolo)
			if err != nil {
				t.Fatalf("writeProviderConfig: %v", err)
			}

			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("config file not readable: %v", err)
			}
			var cfg struct {
				Permission map[string]string `json:"permission"`
				Provider   map[string]struct {
					NPM       string                       `json:"npm"`
					Name      string                       `json:"name"`
					Options   map[string]string            `json:"options"`
					Models    map[string]map[string]string `json:"models"`
					Whitelist []string                     `json:"whitelist"`
				} `json:"provider"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("json: %v", err)
			}
			if len(cfg.Permission) != len(tt.wantPermission) {
				t.Errorf("permission = %+v, want %+v", cfg.Permission, tt.wantPermission)
			}
			for key, want := range tt.wantPermission {
				if got := cfg.Permission[key]; got != want {
					t.Errorf("permission[%q] = %q, want %q", key, got, want)
				}
			}
			configProviderID := providerConfigID(tt.provider)
			prov, ok := cfg.Provider[configProviderID]
			if !ok {
				t.Fatalf("provider %q missing from config", configProviderID)
			}
			if prov.NPM != tt.wantNPM {
				t.Errorf("npm = %q, want %q", prov.NPM, tt.wantNPM)
			}
			wantName := "Aperture (" + tt.provider.ID + ")"
			if prov.Name != wantName {
				t.Errorf("name = %q, want %q", prov.Name, wantName)
			}
			for k, want := range tt.wantOptions {
				if got := prov.Options[k]; got != want {
					t.Errorf("options[%q] = %q, want %q", k, got, want)
				}
			}
			if len(prov.Models) != len(tt.provider.Models) {
				t.Errorf("models len = %d, want %d", len(prov.Models), len(tt.provider.Models))
			}
			for _, m := range tt.provider.Models {
				entry, ok := prov.Models[m]
				if !ok {
					t.Errorf("model %q missing from config", m)
					continue
				}
				if entry["id"] != m {
					t.Errorf("model %q id = %q, want %q", m, entry["id"], m)
				}
				fqn := configProviderID + "/" + m
				if entry["name"] != fqn {
					t.Errorf("model %q name = %q, want %q", m, entry["name"], fqn)
				}
			}
			if len(prov.Whitelist) != len(tt.provider.Models) {
				t.Errorf("whitelist = %v, want %v", prov.Whitelist, tt.provider.Models)
			} else {
				for i, want := range tt.provider.Models {
					if got := prov.Whitelist[i]; got != want {
						t.Errorf("whitelist[%d] = %q, want %q", i, got, want)
					}
				}
			}

			cleanup()
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Errorf("config file still exists after cleanup")
			}
		})
	}
}

func TestCleanupStaleConfigs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, "aperture-old.json")
	fresh := filepath.Join(dir, "aperture-fresh.json")
	unrelated := filepath.Join(dir, "other.json")
	for _, path := range []string{old, fresh, unrelated} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStaleConfigs(dir, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old config was not removed: %v", err)
	}
	for _, path := range []string{fresh, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved file %s: %v", path, err)
		}
	}
}

func TestWriteProviderConfigUsesUniquePrivateFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := config.ProviderInfo{ID: "openrouter", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}}
	first, cleanFirst, err := writeProviderConfig(testHost, p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanFirst()
	second, cleanSecond, err := writeProviderConfig(testHost, p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanSecond()
	if first == second {
		t.Fatalf("temporary config paths are identical: %s", first)
	}
	for _, path := range []string{first, second} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode for %s = %o, want 600", path, got)
		}
	}
}
