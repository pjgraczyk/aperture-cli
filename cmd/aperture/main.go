package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/cli"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/profiles"
	"github.com/tailscale/aperture-cli/internal/tui"

	// Side-effect imports register each client with internal/clients.
	_ "github.com/tailscale/aperture-cli/internal/clients/claudecode"
	_ "github.com/tailscale/aperture-cli/internal/clients/codex"
	_ "github.com/tailscale/aperture-cli/internal/clients/copilot"
	_ "github.com/tailscale/aperture-cli/internal/clients/gemini"
	_ "github.com/tailscale/aperture-cli/internal/clients/opencode"
)

var (
	buildVersion = "B0-dev"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	if buildVersion == "B0-dev" {
		if height := gitCommitHeight(); height != "" {
			buildVersion = "B" + height
		} else if info.Main.Version != "" && info.Main.Version != "(devel)" {
			buildVersion = info.Main.Version
		}
	}

	// Only fill in VCS info when ldflags haven't already set these values.
	if buildCommit != "unknown" {
		return
	}

	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				buildCommit = s.Value[:7]
			}
		case "vcs.time":
			if buildDate == "unknown" {
				buildDate = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty && buildCommit != "unknown" {
		buildCommit += "-dirty"
	}
}

func gitCommitHeight() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return gitCommitHeightInDir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}

func gitCommitHeightInDir(dir string) string {
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	height := strings.TrimSpace(string(out))
	if height == "" {
		return ""
	}
	for _, r := range height {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return height
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "models" || args[0] == "doctor") {
		return runInspect(args, stdout, stderr)
	}

	fs := flag.NewFlagSet("aperture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.Bool("version", false, "print version and exit")
	debugMode := fs.Bool("debug", false, "print env vars set before launching agent")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: aperture [--debug] | aperture --version | aperture <models|doctor> [options]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}

	if *version {
		if buildCommit != "unknown" {
			fmt.Fprintf(stdout, "%s (%s, %s)\n", buildVersion, buildCommit, buildDate)
		} else {
			fmt.Fprintln(stdout, buildVersion)
		}
		return 0
	}

	g, err := config.Load()
	if err != nil {
		slog.Error("loading launcher config", "err", err)
		return 1
	}
	g.Debug = *debugMode

	// Register Claude Desktop on supported platforms (darwin, windows).
	profiles.RegisterIfSupported()

	bridgeManager := bridges.NewManager(g.Debug)
	p := tea.NewProgram(tui.NewModel(g, buildVersion, bridgeManager))

	var exitCode int
	if _, err := p.Run(); err != nil {
		slog.Error("launcher error", "err", err)
		exitCode = 1
	}
	if err := bridgeManager.Close(); err != nil {
		slog.Error("shutting down bridges", "err", err)
		exitCode = 1
	}
	return exitCode
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	g, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "loading launcher config:", err)
		return 1
	}
	bridgeManager := bridges.NewManager(false)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runner := &cli.Runner{
		Global:  g,
		Bridges: bridgeManager,
		Stdout:  stdout,
		Stderr:  stderr,
	}
	exitCode := runner.Run(ctx, args)
	if err := bridgeManager.Close(); err != nil {
		fmt.Fprintln(stderr, "shutting down bridges:", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	return exitCode
}
