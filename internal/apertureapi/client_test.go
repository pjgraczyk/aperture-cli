package apertureapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestClientCatalogs(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/api/providers":
			body = `[{"id":"p","name":"Provider","models":["m"],"compatibility":{"openai_chat":true}}]`
		case "/v1/models":
			body = `{"object":"list","data":[{"id":"m","display_name":"Model","supported_endpoints":["/v1/chat/completions"],"context_window_tokens":1000,"metadata":{"provider":{"id":"p","name":"Provider"}},"pricing":{"input":"0.000001"}}]}`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}

	providers, err := client.Providers(context.Background(), "https://example.test/")
	if err != nil || len(providers) != 1 || providers[0].ID != "p" {
		t.Fatalf("Providers() = %+v, %v", providers, err)
	}
	models, err := client.Models(context.Background(), "https://example.test/")
	if err != nil || len(models) != 1 {
		t.Fatalf("Models() = %+v, %v", models, err)
	}
	if models[0].QualifiedID != "p/m" {
		t.Fatalf("QualifiedID = %q, want p/m", models[0].QualifiedID)
	}
	if models[0].Pricing["input"] != "0.000001" {
		t.Fatalf("pricing lost precision: %+v", models[0].Pricing)
	}
}

func TestClientErrors(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{"status", http.StatusBadGateway, "bad gateway", "unexpected status 502"},
		{"json", http.StatusOK, "not json", "could not parse response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.code, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}}
			_, err := client.Models(context.Background(), "https://example.test")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Models() error = %v, want %q", err, tt.want)
			}
		})
	}
}
