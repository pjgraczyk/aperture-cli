package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/apertureapi"
	"github.com/tailscale/aperture-cli/internal/config"
)

type fakeAPI struct {
	providers []config.ProviderInfo
	models    []apertureapi.Model
	provErr   error
	modelErr  error
	hosts     []string
}

func (f *fakeAPI) Providers(_ context.Context, host string) ([]config.ProviderInfo, error) {
	f.hosts = append(f.hosts, host+"/api/providers")
	return f.providers, f.provErr
}

func (f *fakeAPI) Models(_ context.Context, host string) ([]apertureapi.Model, error) {
	f.hosts = append(f.hosts, host+"/v1/models")
	return append([]apertureapi.Model(nil), f.models...), f.modelErr
}

type fakeBridge struct {
	host string
	err  error
}

func (f fakeBridge) Activate(_ context.Context, _ config.Bridge, _ string, logf func(string)) (string, error) {
	logf("Bridge connected.")
	return f.host, f.err
}

func testRunner(api *fakeAPI) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	g := &config.Global{Settings: config.Settings{Endpoints: []config.Endpoint{{URL: "https://aperture.test"}}}}
	return &Runner{Global: g, API: api, Stdout: stdout, Stderr: stderr}, stdout, stderr
}

func modelFixture() apertureapi.Model {
	return apertureapi.Model{
		ID:                  "model",
		DisplayName:         "Model Name",
		QualifiedID:         "provider/model",
		ContextWindowTokens: 200000,
		SupportedEndpoints:  []string{"/v1/responses"},
		Metadata: apertureapi.ModelMetadata{Provider: apertureapi.ModelProvider{
			ID: "provider", Name: "Provider",
		}},
		Pricing: map[string]string{"input": "0.000002", "output": "0.000010"},
	}
}

func TestModelsTableAndJSON(t *testing.T) {
	api := &fakeAPI{models: []apertureapi.Model{modelFixture()}}
	runner, stdout, stderr := testRunner(api)
	if code := runner.Run(context.Background(), []string{"models"}); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"provider/model", "Model Name", "$2", "$10"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if code := runner.Run(context.Background(), []string{"models", "--json"}); code != exitOK {
		t.Fatalf("JSON exit = %d, stderr = %s", code, stderr.String())
	}
	var payload struct {
		Version  int                 `json:"version"`
		Endpoint string              `json:"endpoint"`
		Models   []apertureapi.Model `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || payload.Endpoint != "https://aperture.test" || len(payload.Models) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestModelsEndpointOverride(t *testing.T) {
	api := &fakeAPI{models: []apertureapi.Model{modelFixture()}}
	runner, _, stderr := testRunner(api)
	if code := runner.Run(context.Background(), []string{"models", "--endpoint", "https://other.test"}); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if got := api.hosts[0]; got != "https://other.test/v1/models" {
		t.Fatalf("host = %q", got)
	}
}

func TestModelsBridge(t *testing.T) {
	api := &fakeAPI{models: []apertureapi.Model{modelFixture()}}
	runner, _, stderr := testRunner(api)
	runner.Global.Settings.Endpoints[0].BridgeID = "bridge-abcdef"
	runner.Global.Settings.Bridges = []config.Bridge{{ID: "bridge-abcdef", Name: "Work"}}
	runner.Bridges = fakeBridge{host: "http://127.0.0.1:1234"}
	if code := runner.Run(context.Background(), []string{"models", "--json"}); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if got := api.hosts[0]; got != "http://127.0.0.1:1234/v1/models" {
		t.Fatalf("host = %q", got)
	}
	if !strings.Contains(stderr.String(), "Bridge connected") {
		t.Fatalf("bridge progress not on stderr: %q", stderr.String())
	}
}

func TestDoctorPassWarningAndFailure(t *testing.T) {
	provider := config.ProviderInfo{ID: "provider", Models: []string{"model"}}
	api := &fakeAPI{providers: []config.ProviderInfo{provider}, models: []apertureapi.Model{modelFixture()}}
	runner, stdout, stderr := testRunner(api)
	if code := runner.Run(context.Background(), []string{"doctor"}); code != exitOK {
		t.Fatalf("pass exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PASS") {
		t.Fatalf("doctor output = %s", stdout.String())
	}

	api.models[0].Pricing = nil
	stdout.Reset()
	if code := runner.Run(context.Background(), []string{"doctor"}); code != exitOK {
		t.Fatalf("warning exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "WARN") {
		t.Fatalf("warning output = %s", stdout.String())
	}

	api.provErr = errors.New("offline")
	stdout.Reset()
	if code := runner.Run(context.Background(), []string{"doctor", "--json"}); code != exitError {
		t.Fatalf("failure exit = %d", code)
	}
	if !strings.Contains(stdout.String(), `"status": "fail"`) {
		t.Fatalf("failure JSON = %s", stdout.String())
	}
}

func TestDoctorArgumentsAndAll(t *testing.T) {
	api := &fakeAPI{}
	runner, _, stderr := testRunner(api)
	if code := runner.Run(context.Background(), []string{"doctor", "--all", "--endpoint", "https://other.test"}); code != exitUsage {
		t.Fatalf("conflict exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	api.providers = []config.ProviderInfo{}
	api.models = []apertureapi.Model{}
	runner.Global.Settings.Endpoints = []config.Endpoint{{URL: "https://one.test"}, {URL: "https://two.test"}}
	if code := runner.Run(context.Background(), []string{"doctor", "--all"}); code != exitOK {
		t.Fatalf("all exit = %d", code)
	}
	if len(api.hosts) != 4 {
		t.Fatalf("requests = %v", api.hosts)
	}
}

func TestInvalidEndpoint(t *testing.T) {
	runner, _, stderr := testRunner(&fakeAPI{})
	if code := runner.Run(context.Background(), []string{"models", "--endpoint", "file:///tmp/api"}); code != exitError {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "http or https") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
