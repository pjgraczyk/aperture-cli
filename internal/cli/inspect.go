// Package cli implements non-interactive Aperture CLI commands.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tailscale/aperture-cli/internal/apertureapi"
	"github.com/tailscale/aperture-cli/internal/config"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// BridgeActivator activates the local proxy for a configured bridge.
type BridgeActivator interface {
	Activate(context.Context, config.Bridge, string, func(string)) (string, error)
}

// API is the read-only subset of the Aperture API used by inspect commands.
type API interface {
	Providers(context.Context, string) ([]config.ProviderInfo, error)
	Models(context.Context, string) ([]apertureapi.Model, error)
}

// Runner owns dependencies for non-interactive commands.
type Runner struct {
	Global  *config.Global
	API     API
	Bridges BridgeActivator
	Stdout  io.Writer
	Stderr  io.Writer
}

// Run executes a supported non-interactive command and returns a process exit
// code. The caller is responsible for closing the bridge manager.
func (r *Runner) Run(ctx context.Context, args []string) int {
	if r.API == nil {
		r.API = apertureapi.NewClient()
	}
	if r.Stdout == nil {
		r.Stdout = io.Discard
	}
	if r.Stderr == nil {
		r.Stderr = io.Discard
	}
	if len(args) == 0 {
		return exitUsage
	}
	switch args[0] {
	case "models":
		return r.runModels(ctx, args[1:])
	case "doctor":
		return r.runDoctor(ctx, args[1:])
	default:
		fmt.Fprintf(r.Stderr, "unknown command %q\n", args[0])
		return exitUsage
	}
}

func (r *Runner) runModels(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	endpoint := fs.String("endpoint", "", "inspect a direct Aperture endpoint URL")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	fs.Usage = func() {
		fmt.Fprintln(r.Stderr, "usage: aperture models [--endpoint URL] [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		if err == nil {
			fs.Usage()
		}
		return exitUsage
	}
	ep := r.activeEndpoint()
	if *endpoint != "" {
		ep = config.Endpoint{URL: *endpoint}
	}
	host, err := r.resolveEndpoint(ctx, ep)
	if err != nil {
		fmt.Fprintln(r.Stderr, "models:", err)
		return exitError
	}
	models, err := r.API.Models(ctx, host)
	if err != nil {
		fmt.Fprintln(r.Stderr, "models:", err)
		return exitError
	}
	sort.Slice(models, func(i, j int) bool { return models[i].QualifiedID < models[j].QualifiedID })
	if *jsonOutput {
		payload := struct {
			Version  int                 `json:"version"`
			Endpoint string              `json:"endpoint"`
			Models   []apertureapi.Model `json:"models"`
		}{Version: 1, Endpoint: ep.URL, Models: models}
		if err := writeJSON(r.Stdout, payload); err != nil {
			fmt.Fprintln(r.Stderr, "models:", err)
			return exitError
		}
		return exitOK
	}
	w := tabwriter.NewWriter(r.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tNAME\tCONTEXT\tENDPOINTS\tINPUT / 1M\tOUTPUT / 1M")
	for _, model := range models {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			model.QualifiedID,
			fallback(model.DisplayName, model.ID),
			formatTokens(model.ContextWindowTokens),
			strings.Join(model.SupportedEndpoints, ","),
			formatPrice(model.Pricing["input"]),
			formatPrice(model.Pricing["output"]),
		)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(r.Stderr, "models:", err)
		return exitError
	}
	return exitOK
}

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type doctorEndpoint struct {
	Endpoint string        `json:"endpoint"`
	BridgeID string        `json:"bridge_id,omitempty"`
	Status   string        `json:"status"`
	Checks   []doctorCheck `json:"checks"`
}

func (r *Runner) runDoctor(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	endpoint := fs.String("endpoint", "", "inspect a direct Aperture endpoint URL")
	all := fs.Bool("all", false, "inspect every configured endpoint")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	fs.Usage = func() {
		fmt.Fprintln(r.Stderr, "usage: aperture doctor [--endpoint URL | --all] [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		if err == nil {
			fs.Usage()
		}
		return exitUsage
	}
	if *endpoint != "" && *all {
		fmt.Fprintln(r.Stderr, "doctor: --endpoint and --all are mutually exclusive")
		return exitUsage
	}
	endpoints := []config.Endpoint{r.activeEndpoint()}
	if *all {
		endpoints = append([]config.Endpoint(nil), r.Global.Settings.Endpoints...)
		if len(endpoints) == 0 {
			endpoints = []config.Endpoint{{URL: config.DefaultLocation}}
		}
	}
	if *endpoint != "" {
		endpoints = []config.Endpoint{{URL: *endpoint}}
	}
	results := make([]doctorEndpoint, 0, len(endpoints))
	failed := false
	for _, ep := range endpoints {
		result := r.checkEndpoint(ctx, ep)
		if result.Status == "fail" {
			failed = true
		}
		results = append(results, result)
	}
	if *jsonOutput {
		payload := struct {
			Version int              `json:"version"`
			Results []doctorEndpoint `json:"results"`
		}{Version: 1, Results: results}
		if err := writeJSON(r.Stdout, payload); err != nil {
			fmt.Fprintln(r.Stderr, "doctor:", err)
			return exitError
		}
	} else {
		r.writeDoctorTable(results)
	}
	if failed {
		return exitError
	}
	return exitOK
}

func (r *Runner) checkEndpoint(ctx context.Context, ep config.Endpoint) doctorEndpoint {
	result := doctorEndpoint{Endpoint: ep.URL, BridgeID: ep.BridgeID, Status: "pass"}
	add := func(name, status, message string) {
		result.Checks = append(result.Checks, doctorCheck{Name: name, Status: status, Message: message})
		if status == "fail" {
			result.Status = "fail"
		} else if status == "warn" && result.Status == "pass" {
			result.Status = "warn"
		}
	}
	if err := validateEndpointURL(ep.URL); err != nil {
		add("endpoint URL", "fail", err.Error())
		return result
	}
	add("endpoint URL", "pass", ep.URL)

	host, err := r.resolveEndpoint(ctx, ep)
	if err != nil {
		name := "direct endpoint"
		if ep.BridgeID != "" {
			name = "bridge"
		}
		add(name, "fail", err.Error())
		return result
	}
	if ep.BridgeID != "" {
		add("bridge", "pass", ep.BridgeID)
	}

	providers, providersErr := r.API.Providers(ctx, host)
	if providersErr != nil {
		add("providers", "fail", providersErr.Error())
	} else {
		add("providers", "pass", fmt.Sprintf("%d providers", len(providers)))
	}
	models, modelsErr := r.API.Models(ctx, host)
	if modelsErr != nil {
		add("models", "fail", modelsErr.Error())
	} else {
		add("models", "pass", fmt.Sprintf("%d models", len(models)))
	}
	if providersErr == nil && modelsErr == nil {
		if warnings := catalogWarnings(providers, models); len(warnings) > 0 {
			add("catalog consistency", "warn", strings.Join(warnings, "; "))
		} else {
			add("catalog consistency", "pass", "provider and model catalogs agree")
		}
		missingPricing := 0
		for _, model := range models {
			if len(model.Pricing) == 0 {
				missingPricing++
			}
		}
		if missingPricing > 0 {
			add("optional metadata", "warn", fmt.Sprintf("%d models have no pricing", missingPricing))
		} else {
			add("optional metadata", "pass", "pricing is available for every model")
		}
	}
	return result
}

func (r *Runner) writeDoctorTable(results []doctorEndpoint) {
	w := tabwriter.NewWriter(r.Stdout, 0, 4, 2, ' ', 0)
	for i, result := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s\t%s\n", strings.ToUpper(result.Status), result.Endpoint)
		for _, check := range result.Checks {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", strings.ToUpper(check.Status), check.Name, check.Message)
		}
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(r.Stderr, "doctor:", err)
	}
}

func (r *Runner) activeEndpoint() config.Endpoint {
	if r.Global == nil {
		return config.Endpoint{URL: config.DefaultLocation}
	}
	return r.Global.ActiveEndpoint()
}

func (r *Runner) resolveEndpoint(ctx context.Context, ep config.Endpoint) (string, error) {
	if err := validateEndpointURL(ep.URL); err != nil {
		return "", err
	}
	if ep.BridgeID == "" {
		return ep.URL, nil
	}
	if r.Global == nil {
		return "", fmt.Errorf("bridge %s is not configured", ep.BridgeID)
	}
	bridge, ok := r.Global.Bridge(ep.BridgeID)
	if !ok {
		return "", fmt.Errorf("bridge %s is not configured", ep.BridgeID)
	}
	if r.Bridges == nil {
		return "", fmt.Errorf("bridge manager is not configured")
	}
	return r.Bridges.Activate(ctx, bridge, ep.URL, func(line string) {
		line = strings.TrimSpace(line)
		if line != "" {
			fmt.Fprintln(r.Stderr, line)
		}
	})
}

func validateEndpointURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint URL must use http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("endpoint URL must include a host")
	}
	return nil
}

func catalogWarnings(providers []config.ProviderInfo, models []apertureapi.Model) []string {
	basic := make(map[string]bool)
	for _, provider := range providers {
		for _, model := range provider.Models {
			basic[provider.ID+"\x00"+model] = true
		}
	}
	rich := make(map[string]bool, len(models))
	for _, model := range models {
		rich[model.Metadata.Provider.ID+"\x00"+model.ID] = true
	}
	var missing, extra []string
	for _, provider := range providers {
		for _, model := range provider.Models {
			if !rich[provider.ID+"\x00"+model] {
				missing = append(missing, provider.ID+"/"+model)
			}
		}
	}
	for _, model := range models {
		key := model.Metadata.Provider.ID + "\x00" + model.ID
		if !basic[key] {
			extra = append(extra, model.QualifiedID)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var warnings []string
	if len(missing) > 0 {
		warnings = append(warnings, summarizeMismatch("provider models missing from /v1/models", missing))
	}
	if len(extra) > 0 {
		warnings = append(warnings, summarizeMismatch("/v1/models entries missing from /api/providers", extra))
	}
	return warnings
}

func summarizeMismatch(label string, values []string) string {
	const maxReported = 3
	shown := values
	if len(shown) > maxReported {
		shown = shown[:maxReported]
	}
	message := fmt.Sprintf("%d %s: %s", len(values), label, strings.Join(shown, ", "))
	if len(values) > maxReported {
		message += ", …"
	}
	return message
}

func formatTokens(n int64) string {
	if n <= 0 {
		return "—"
	}
	return strconv.FormatInt(n, 10)
}

func formatPrice(raw string) string {
	if raw == "" {
		return "—"
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return "$" + strconv.FormatFloat(value*1_000_000, 'f', -1, 64)
}

func fallback(value, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}
	return nil
}
