// Package opencode is the OpenCode client. Unlike the other clients,
// OpenCode has a single abstract routing flavor: the real protocol (OpenAI
// Responses, OpenAI Chat, Anthropic Messages, Bedrock, Vertex, Gemini) is
// decided at launch time from the chosen provider's advertised endpoints. The
// Menu flow selects both a provider and a model, then launches OpenCode with
// that model already active.
package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

func init() {
	clients.Register(&Client{})
}

// Client is the OpenCode client.
type Client struct{}

const (
	name              = "OpenCode"
	binaryName        = "opencode"
	codexAuthPlugin   = "opencode-openai-codex-auth@latest"
	codexAuthProvider = "openai"
)

// supportedEndpoints is the set of Aperture endpoints OpenCode can translate
// into a working config. A provider matches if any one is advertised.
var supportedEndpoints = []string{
	config.EndpointOpenAIResponses,
	config.EndpointAnthropicMessages,
	config.EndpointOpenAIChat,
	config.EndpointVertexGemini,
	config.EndpointVertexClaude,
	config.EndpointBedrockInvoke,
	config.EndpointBedrockConverse,
	config.EndpointGemini,
}

// Name implements clients.Client.
func (c *Client) Name() string { return name }

// BinaryName implements clients.Client.
func (c *Client) BinaryName() string { return binaryName }

// CommonPaths implements clients.Client.
func (c *Client) CommonPaths() []string { return commonBinaryPaths() }

// IsInstalled implements clients.Client.
func (c *Client) IsInstalled() bool {
	return clients.IsInstalled(binaryName, c.CommonPaths())
}

// Install implements clients.Client.
func (c *Client) Install(_ *config.Global) clients.InstallPlan {
	return clients.InstallPlan{
		Hint: "curl -fsSL https://opencode.ai/install | bash",
		Run: func() (*exec.Cmd, error) {
			return exec.Command("bash", "-o", "pipefail", "-c", "curl -fsSL https://opencode.ai/install | bash"), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: "opencode uninstall --force\nrm -rf ~/.opencode/bin",
		Run: func() error {
			if err := exec.Command("opencode", "uninstall", "--force").Run(); err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return os.RemoveAll(filepath.Join(home, ".opencode", "bin"))
		},
	}
}

// Menu implements clients.Client.
func (c *Client) Menu(g *config.Global) menu.MenuItem {
	return menu.MenuItem{
		Label:  name,
		Action: func() menu.Result { return c.providerStep(g) },
	}
}

func (c *Client) providerStep(g *config.Global) menu.Result {
	provs := compatibleProviders(g.Providers)
	if len(provs) == 0 {
		return errorResult("No providers support an OpenCode protocol.")
	}
	if len(provs) == 1 {
		return c.providerAuthStep(g, provs[0])
	}
	items := make([]menu.MenuItem, 0, len(provs))
	for _, p := range provs {
		items = append(items, menu.MenuItem{
			Label:       p.DisplayName(),
			Description: p.Description,
			Action:      func() menu.Result { return c.providerAuthStep(g, p) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a provider for " + name + ":",
		Items: items,
	}}
}

func (c *Client) providerAuthStep(g *config.Global, p config.ProviderInfo) menu.Result {
	if !p.RequiresClientAuth {
		return c.modelStep(g, p)
	}
	state, err := detectAuthSetup()
	if err != nil {
		return errorResult("Could not inspect OpenCode authentication: " + err.Error())
	}
	if state.pluginInstalled && state.authenticated {
		return c.modelStep(g, p)
	}
	if !state.pluginInstalled {
		if _, err := exec.LookPath("npx"); err != nil {
			return errorResult("OpenCode's ChatGPT auth plugin is missing, and npx was not found. Install Node.js, then try again.")
		}
		return menu.Result{Next: &menu.Menu{
			Title: "Install ChatGPT authentication for OpenCode?",
			Preamble: "This provider needs a personal ChatGPT Plus/Pro OAuth token. " +
				codexAuthPluginName + " is a third-party plugin intended for personal use.",
			Items: []menu.MenuItem{
				{
					Label:       "Install the ChatGPT auth plugin",
					Description: "Runs: npx -y " + codexAuthPlugin,
					Action: func() menu.Result {
						return menu.Result{Cmd: setupCommand(func() menu.Result {
							return refreshSetupResult(c.providerAuthStep(g, p))
						}, "npx", "-y", codexAuthPlugin)}
					},
				},
				{Label: "Cancel", Action: func() menu.Result { return menu.Result{Pop: true} }},
			},
		}}
	}
	return menu.Result{Next: &menu.Menu{
		Title:    "Authenticate OpenCode with ChatGPT?",
		Preamble: "The auth plugin is installed, but OpenCode has no saved OpenAI authentication.",
		Items: []menu.MenuItem{
			{
				Label:       "Authenticate with ChatGPT",
				Description: "Runs OpenCode's interactive auth login",
				Action: func() menu.Result {
					bin := clients.FindBinary(binaryName, c.CommonPaths())
					if bin == "" {
						bin = binaryName
					}
					return menu.Result{Cmd: setupCommand(func() menu.Result {
						return refreshSetupResult(c.providerAuthStep(g, p))
					}, bin, "auth", "login")}
				},
			},
			{Label: "Cancel", Action: func() menu.Result { return menu.Result{Pop: true} }},
		},
	}}
}

// modelStep shows the model picker when the provider has multiple models, or
// launches directly when it exposes zero or one model.
func (c *Client) modelStep(g *config.Global, p config.ProviderInfo) menu.Result {
	models := fqnModels(p)
	if len(models) <= 1 {
		var model string
		if len(models) == 1 {
			model = models[0]
		}
		return c.launch(g, p, model)
	}
	items := make([]menu.MenuItem, 0, len(models))
	for _, model := range models {
		items = append(items, menu.MenuItem{
			Label:  model,
			Action: func() menu.Result { return c.launch(g, p, model) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a default model for " + name + " via " + p.DisplayName() + ":",
		Items: items,
	}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, model string) menu.Result {
	// Validate locally against the provider's advertised models. The temp
	// config written below exposes exactly fqnModels(p), so anything else
	// can never resolve. (opencode v2 removed the `models <provider>`
	// filter, and its background server may serve a stale config, so a CLI
	// round-trip cannot reliably validate.)
	if model != "" && !slices.Contains(fqnModels(p), model) {
		return errorResult(fmt.Sprintf("model %q is not available for provider %q", model, p.DisplayName()))
	}
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	configPath, cleanup, err := writeProviderConfig(g.ApertureHost, p, model, g.Settings.YoloMode)
	if err != nil {
		return errorResult("Failed to write OpenCode config: " + err.Error())
	}

	env := map[string]string{
		"OPENCODE_CONFIG": configPath,
	}
	// Bedrock SDK requires at least placeholder AWS credentials and region.
	if p.SupportsEndpoint(config.EndpointBedrockInvoke) || p.SupportsEndpoint(config.EndpointBedrockConverse) {
		env["AWS_ACCESS_KEY_ID"] = "not-needed"
		env["AWS_SECRET_ACCESS_KEY"] = "not-needed"
		env["AWS_REGION"] = "us-east-1"
	}

	// The selected model is conveyed via the config file's top-level
	// "model" default: opencode v2 has no top-level --model flag (it only
	// exists on `run`), so launch bare opencode with no args.
	spec := clients.LaunchSpec{
		Binary:  bin,
		Env:     env,
		Cleanup: cleanup,
		Debug:   g.Debug,
	}
	return menu.Result{Cmd: func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := resolveModels(ctx, bin, env, providerConfigID(p), model); err != nil {
			return menu.ContinueMsg{Err: fmt.Errorf("OpenCode could not use the selected model: %w", errCleanup(err, cleanup))}
		}
		previousLaunch := g.LastLaunch
		if err := g.RecordLaunch(config.LaunchState{
			LastClientName: name, LastBackendType: "openai",
			LastProviderID: p.ID, LastModel: model,
		}); err != nil {
			g.LastLaunch = previousLaunch
			return menu.ContinueMsg{Err: fmt.Errorf("save OpenCode launch state: %w", errCleanup(err, cleanup))}
		}
		return menu.ContinueMsg{Result: menu.Result{Cmd: clients.Launch(spec), PopOnDone: true}}
	}}
}

// Replay implements clients.Client.
func (c *Client) Replay(g *config.Global) tea.Cmd {
	if g.LastLaunch.LastClientName != name || !c.IsInstalled() {
		return nil
	}
	prov, ok := g.Provider(g.LastLaunch.LastProviderID)
	if !ok {
		return nil
	}
	if !providerMatches(prov) {
		return nil
	}
	model := g.LastLaunch.LastModel
	if model != "" && !slices.Contains(fqnModels(prov), model) {
		return nil
	}
	res := c.launch(g, prov, model)
	return res.Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	label := name + " via " + prov.DisplayName()
	if g.LastLaunch.LastModel != "" {
		label += " - " + g.LastLaunch.LastModel
	}
	return label
}

func fqnModels(p config.ProviderInfo) []string {
	out := make([]string, len(p.Models))
	providerID := providerConfigID(p)
	for i, model := range p.Models {
		out[i] = providerID + "/" + model
	}
	return out
}

func compatibleProviders(all []config.ProviderInfo) []config.ProviderInfo {
	var out []config.ProviderInfo
	for _, p := range all {
		if providerMatches(p) {
			out = append(out, p)
		}
	}
	return out
}

func providerMatches(p config.ProviderInfo) bool {
	for _, endpoint := range supportedEndpoints {
		if p.SupportsEndpoint(endpoint) {
			return true
		}
	}
	return false
}

func providerConfigID(p config.ProviderInfo) string {
	if p.RequiresClientAuth {
		return codexAuthProvider
	}
	return p.ID
}

func setupCommand(next func() menu.Result, binary string, args ...string) tea.Cmd {
	cmd := exec.Command(binary, args...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return menu.ContinueMsg{Err: err}
		}
		return menu.ContinueMsg{Result: next()}
	})
}

func refreshSetupResult(result menu.Result) menu.Result {
	if result.Next != nil {
		result.Replace = result.Next
		result.Next = nil
	}
	return result
}

var resolveModels = defaultResolveModels

func defaultResolveModels(ctx context.Context, binary string, env map[string]string, _, _ string) error {
	// Best-effort connectivity check only. opencode v2 removed the optional
	// `models <provider>` positional filter, and the background server may
	// serve a stale config that does not include our just-written temp
	// provider, so model membership must NOT be checked here (launch()
	// already validates locally). Only a failing CLI itself is an error.
	cmd := exec.CommandContext(ctx, binary, "models")
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("model check timed out after 30 seconds")
	}
	if err != nil {
		return fmt.Errorf("model check failed: %s", boundedDiagnostic(output))
	}
	return nil
}

func modelOutputContains(output []byte, selected string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == selected {
			return true
		}
	}
	return false
}

func boundedDiagnostic(output []byte) string {
	const limit = 4096
	output = []byte(strings.TrimSpace(string(output)))
	if len(output) == 0 {
		return "no diagnostic output"
	}
	if len(output) > limit {
		output = append(output[:limit], []byte("...")...)
	}
	return string(output)
}

func errCleanup(primary error, cleanup func() error) error {
	if cleanupErr := cleanup(); cleanupErr != nil {
		return fmt.Errorf("%v; cleanup temporary config: %w", primary, cleanupErr)
	}
	return primary
}

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
