package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if code := run([]string{"--version"}, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), buildVersion) {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestRunUnknownArgument(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if code := run([]string{"unknown"}, stdout, stderr); code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: aperture") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
