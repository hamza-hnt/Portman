package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
	"portman/internal/discovery"
	"portman/internal/docker"
	"portman/internal/engine"
	"portman/internal/platform"
	"portman/internal/process"
	"portman/internal/tui"
)

var version = "dev"

func run() error {
	interval := flag.Duration("interval", 2*time.Second, "automatic refresh interval (minimum 500ms)")
	all := flag.Bool("all", false, "include non-listening TCP connections")
	jsonOutput := flag.Bool("json", false, "print one JSON snapshot and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	noMouse := flag.Bool("no-mouse", false, "disable mouse capture for terminal text selection")
	flag.Parse()
	if *showVersion {
		fmt.Println("portman " + version)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments; use portman --help")
	}
	if *interval < 500*time.Millisecond {
		return fmt.Errorf("--interval must be at least 500ms")
	}
	if err := platform.Supported(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspector := platform.Inspector{}
	manager := process.New(inspector)
	scanner := &engine.Engine{Discovery: discovery.New(inspector), Inspector: inspector, Manager: manager, Docker: docker.New()}
	if *jsonOutput {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		s, err := scanner.Scan(ctx, *all)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(s)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("interactive mode requires a terminal; use --json for scripts")
	}
	options := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	if !*noMouse {
		options = append(options, tea.WithMouseCellMotion())
	}
	_, err := tea.NewProgram(tui.New(ctx, scanner, scanner, *interval, *all), options...).Run()
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "portman:", err)
		os.Exit(1)
	}
}
