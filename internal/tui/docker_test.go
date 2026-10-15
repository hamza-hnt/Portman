package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"portman/internal/model"
)

func dockerFixture() Model {
	m := New(context.Background(), nil, nil, time.Second, false)
	m.scanning = false
	m.status = "Ready"
	m.width = 124
	m.height = 32
	for i, name := range []string{"shop-api-1", "shop-db-1", "tools-api-1"} {
		project, service := "shop", "api"
		if i == 1 {
			service = "db"
		}
		if i == 2 {
			project = "tools"
		}
		p := model.Process{Name: name, Category: model.Docker, Project: &model.Project{Name: project, Root: "/work/" + project}, Container: &model.Container{ID: strings.Repeat(string(rune('a'+i)), 64), StartedAt: "2026-10-15T10:00:00Z", Endpoint: "unix:///tmp/docker.sock", Image: "example/" + service, ComposeProject: project, Service: service, Ports: []model.PortMapping{{Address: "127.0.0.1", HostPort: 8080 + i, ContainerPort: 80, Protocol: "tcp"}, {Address: "::1", HostPort: 8080 + i, ContainerPort: 80, Protocol: "tcp"}}}, Sockets: []model.Socket{{Address: "127.0.0.1", Port: 8080 + i, Protocol: "TCP", State: "LISTEN"}}}
		m.snapshot.Processes = append(m.snapshot.Processes, p)
	}
	m.snapshot.At = time.Now()
	m.rebuild()
	return m
}

func focusContainer(m *Model, id string) {
	for i, r := range m.rows {
		if r.process != nil && r.port == nil && r.process.Container.ID == id {
			m.cursor = i
			return
		}
	}
}

func TestDockerTreeExpandPortsAndStopOwner(t *testing.T) {
	m := dockerFixture()
	if len(m.rows) != 6 || m.matchedCount() != 3 {
		t.Fatalf("bad nested groups/count: %d/%d", len(m.rows), m.matchedCount())
	}
	if m.rows[0].label != "Docker" || m.rows[1].label != "shop · Compose" || len(m.rows[1].members) != 2 {
		t.Fatal("missing Compose grouping")
	}
	p := m.snapshot.Processes[0]
	focusContainer(&m, p.Container.ID)
	m, _ = key(m, "enter")
	if len(m.rows) != 8 || m.details {
		t.Fatal("Enter must expand ports inline")
	}
	view := ansi.Strip(m.View())
	for _, s := range []string{"127.0.0.1:8080 → 80/tcp", "[::1]:8080 → 80/tcp", "Stop container", "other containers stay running"} {
		if !strings.Contains(view, s) {
			t.Fatalf("missing %q\n%s", s, view)
		}
	}
	m.move(1) // focused port, stop its owner only
	m.askStop()
	if m.modal == nil || len(m.modal.targets) != 1 || m.modal.targets[0].Key() != p.Key() {
		t.Fatal("port stop must target its owner only")
	}
	if !strings.Contains(ansi.Strip(m.View()), "All ports of each listed container close") {
		t.Fatal("missing scope warning")
	}
	m, _ = key(m, "enter")
	if m.modal != nil || m.stopping {
		t.Fatal("default must cancel")
	}
	m.toggleGroup(true)
	if m.rows[m.cursor].key != p.Key() || !m.collapsed[p.Key()] {
		t.Fatal("left must collapse port's container")
	}
	m.toggleGroup(true)
	if m.rows[m.cursor].label != "shop · Compose" || !m.collapsed[m.rows[m.cursor].key] {
		t.Fatal("left must return to Compose parent")
	}
}

func TestDockerFilteredGroupSelectionAndRestart(t *testing.T) {
	m := dockerFixture()
	m.query = "shop 8080"
	m.rebuild()
	if len(m.rows) != 3 || m.matchedCount() != 1 {
		t.Fatal("search did not narrow Compose members")
	}
	m.cursor = 1
	m.toggleSelection()
	m.askStop()
	if len(m.selected) != 1 || len(m.modal.targets) != 1 || m.modal.targets[0].Name != "shop-api-1" {
		t.Fatal("selected unrelated service")
	}
	m.modal = nil
	m.query = "tools"
	m.rebuild()
	if !strings.Contains(ansi.Strip(m.View()), "1 container selected · 1 hidden") {
		t.Fatal("hidden container selection was lost")
	}
	m.snapshot.Processes[0].Container.StartedAt = "2026-10-15T11:00:00Z"
	m.rebuild()
	if len(m.selected) != 0 {
		t.Fatal("restart retained stale selection")
	}
	if !matches(m.snapshot.Processes[0], "shop api 80 example/api aaaaaa") {
		t.Fatal("container metadata search failed")
	}
}

func TestDockerMouseExpansionAndLayout(t *testing.T) {
	m := dockerFixture()
	focusContainer(&m, m.snapshot.Processes[0].Container.ID)
	index := m.cursor
	u, _ := m.Update(tea.MouseMsg{X: 12, Y: tableTop + index, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if len(m.rows) != 8 {
		t.Fatal("click did not expand container")
	}
	u, _ = m.Update(tea.MouseMsg{X: 4, Y: tableTop + index, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if len(m.selected) != 1 || len(m.rows) != 8 {
		t.Fatal("checkbox should select without collapsing")
	}
	for _, size := range [][2]int{{64, 22}, {80, 24}, {100, 30}, {140, 40}} {
		m.width, m.height = size[0], size[1]
		for _, mode := range []string{"main", "dialog", "details"} {
			m.modal = nil
			m.details = false
			if mode == "dialog" {
				m.askStop()
			}
			if mode == "details" {
				m.details = true
			}
			lines := strings.Split(m.View(), "\n")
			if len(lines) != m.height {
				t.Fatalf("%s %v height %d", mode, size, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("%s %v overflow: %q", mode, size, line)
				}
			}
		}
	}
}
