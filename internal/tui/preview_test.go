package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"portman/internal/model"
)

// Export the production renderer with synthetic data for visual QA and docs.
// No demo data is part of the application binary or its discovery path.
func TestExportPreview(t *testing.T) {
	dir := os.Getenv("PORTMAN_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set PORTMAN_PREVIEW_DIR to export visual previews")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)
	m := fixture()
	m.width = 124
	m.height = 32
	m.status = ""
	m.snapshot.At = time.Date(2026, 10, 15, 18, 42, 6, 0, time.Local)
	m.snapshot.Processes[0].Project = &model.Project{Name: "storefront", Root: "~/Projects/storefront"}
	m.snapshot.Processes[0].CWD = "~/Projects/storefront/apps/web"
	m.snapshot.Processes[0].Executable = "/opt/homebrew/bin/node"
	m.snapshot.Processes[0].Name = "node · vite"
	m.snapshot.Processes[1].Project = m.snapshot.Processes[0].Project
	m.snapshot.Processes[1].Name = "api-server"
	m.snapshot.Processes[1].CWD = "~/Projects/storefront/services/api"
	m.snapshot.Processes[1].Executable = "~/Projects/storefront/bin/api-server"
	m.snapshot.Processes = append(m.snapshot.Processes,
		model.Process{Identity: model.Identity{PID: 4208, StartID: 8}, Name: "redis-server", Category: model.Homebrew, Sockets: []model.Socket{{Protocol: "TCP", Port: 6379, Address: "127.0.0.1", State: "LISTEN"}}},
		model.Process{Identity: model.Identity{PID: 4219, StartID: 9}, Name: "postgres", Category: model.Homebrew, Sockets: []model.Socket{{Protocol: "TCP", Port: 5432, Address: "127.0.0.1", State: "LISTEN"}}},
		model.Process{Identity: model.Identity{PID: 6273, StartID: 10}, Name: "com.docker.backend", Category: model.Docker, Sockets: []model.Socket{{Protocol: "TCP", Port: 8000, Address: "*", State: "LISTEN"}}},
	)
	m.rebuild()
	m.cursor = 2
	for _, size := range []struct {
		name string
		w, h int
	}{{"overview", 124, 32}, {"compact", 80, 24}, {"minimum", 64, 22}} {
		m.width = size.w
		m.height = size.h
		if err := os.WriteFile(filepath.Join(dir, size.name+".ansi"), []byte(m.View()), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, size.name+".txt"), []byte(ansi.Strip(m.View())), 0644); err != nil {
			t.Fatal(err)
		}
	}
	m.width = 100
	m.height = 30
	m.askStop()
	if err := os.WriteFile(filepath.Join(dir, "confirmation.ansi"), []byte(m.View()), 0644); err != nil {
		t.Fatal(err)
	}
}
