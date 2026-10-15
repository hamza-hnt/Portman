package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"portman/internal/model"
	"portman/internal/process"
)

func fixture() Model {
	m := New(context.Background(), nil, nil, time.Second, false)
	m.snapshot = model.Snapshot{At: time.Now(), Processes: []model.Process{
		{Identity: model.Identity{PID: 20, StartID: 1}, UID: 501, Name: "node", Executable: "/usr/local/node", CWD: "/work/app/web", Category: model.Development, Project: &model.Project{Name: "app", Root: "/work/app"}, Sockets: []model.Socket{{Protocol: "TCP", Port: 3000, Address: "::1", State: "LISTEN"}}},
		{Identity: model.Identity{PID: 21, StartID: 2}, UID: 501, Name: "server", Executable: "/tmp/server", Category: model.Development, Project: &model.Project{Name: "app", Root: "/work/app"}, Sockets: []model.Socket{{Protocol: "TCP", Port: 8080, Address: "127.0.0.1", State: "LISTEN"}}},
		{Identity: model.Identity{PID: 22, StartID: 3}, UID: 0, Name: "system", Category: model.System, Protected: "system process", Sockets: []model.Socket{{Protocol: "UDP", Port: 5353, Address: "*", State: "BOUND"}}},
	}}
	m.scanning = false
	m.rebuild()
	return m
}

func key(m Model, k string) (Model, tea.Cmd) {
	typeOf := tea.KeyRunes
	var runes []rune
	switch k {
	case "enter":
		typeOf = tea.KeyEnter
	case "esc":
		typeOf = tea.KeyEsc
	case "tab":
		typeOf = tea.KeyTab
	case " ":
		typeOf = tea.KeySpace
	default:
		runes = []rune(k)
	}
	u, c := m.Update(tea.KeyMsg{Type: typeOf, Runes: runes})
	return u.(Model), c
}

func TestSelectionAndDefaultCancel(t *testing.T) {
	m := fixture()
	m, _ = key(m, " ")
	if len(m.selected) != 2 {
		t.Fatal("group selection missing")
	}
	m, _ = key(m, "d")
	if m.modal == nil || len(m.modal.targets) != 2 {
		t.Fatal("group confirmation missing")
	}
	m, cmd := key(m, "enter")
	if m.modal != nil || m.stopping || cmd != nil {
		t.Fatal("Enter must cancel by default")
	}
	m.cursor = len(m.rows) - 1
	m.selected = map[string]bool{}
	m, _ = key(m, "d")
	if m.modal != nil {
		t.Fatal("protected row must not open confirmation")
	}
}

func TestSearchAndStableIdentity(t *testing.T) {
	m := fixture()
	m.cursor = 1
	m.toggleSelection()
	m, _ = key(m, "/")
	m, _ = key(m, "3000 node")
	m, _ = key(m, "enter")
	if len(m.rows) != 2 || !matches(*m.rows[1].process, "3000 node") {
		t.Fatal("search must AND tokens")
	}
	m.query = ""
	m.rebuild()
	m.cursor = 1
	oldKey := m.rows[1].key
	m.snapshot.Processes[0].Identity.StartID = 99
	m.rebuild()
	if m.selected[oldKey] {
		t.Fatal("recycled PID retained selection")
	}
	m.cursor = 2
	focused := m.rows[2].key
	m.rebuild()
	if m.rows[m.cursor].key != focused {
		t.Fatal("refresh moved selection")
	}
}

func TestLayoutSizesAndControlSanitization(t *testing.T) {
	for _, size := range [][2]int{{64, 22}, {80, 24}, {100, 30}, {140, 40}} {
		m := fixture()
		m.width = size[0]
		m.height = size[1]
		for _, mode := range []string{"main", "dialog", "help", "details"} {
			m.modal = nil
			m.help = false
			m.details = false
			switch mode {
			case "dialog":
				m.askStop()
			case "help":
				m.help = true
			case "details":
				m.details = true
			}
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != m.height {
				t.Fatalf("%s %v: height %d want %d\n%s", mode, size, len(lines), m.height, view)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("%s %v: line too wide: %q", mode, size, line)
				}
			}
		}
	}
	if s := clean("bad\x1b]52;c;evil\a\n\u202e"); strings.ContainsAny(s, "\x1b\a\n\u202e") {
		t.Fatal("terminal control injection")
	}
}

func TestMouseAndDialogGeometry(t *testing.T) {
	m := fixture()
	u, _ := m.Update(tea.MouseMsg{X: 4, Y: 9, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if len(m.selected) != 1 || m.cursor != 1 {
		t.Fatal("checkbox click did not select")
	}
	m.askStop()
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	x, y, _, buttonY := m.dialogGeometry()
	if !strings.Contains(lines[y+buttonY], "[n] Cancel") {
		t.Fatalf("button geometry mismatch: y=%d\n%s", y+buttonY, view)
	}
	u, _ = m.Update(tea.MouseMsg{X: x + 5, Y: y + buttonY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if m.modal != nil {
		t.Fatal("cancel button failed")
	}
	stopX := 0
	for _, item := range m.actions() {
		if item.key == "d" {
			stopX = item.start
		}
	}
	u, _ = m.Update(tea.MouseMsg{X: stopX, Y: m.height - 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if m.modal == nil {
		t.Fatal("footer stop button failed")
	}
	x, y, _, buttonY = m.dialogGeometry()
	u, cmd := m.Update(tea.MouseMsg{X: x + 20, Y: y + buttonY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = u.(Model)
	if !m.stopping || m.modal != nil || cmd == nil {
		t.Fatal("confirm button must schedule termination")
	}
}

type fakeTerminator struct {
	calls int
	force bool
}

func (f *fakeTerminator) Terminate(_ context.Context, p []model.Process, force bool) []process.Result {
	f.calls++
	f.force = force
	return []process.Result{{Process: p[0], Status: "running"}}
}

func TestEscalationNeedsSeparateConfirmation(t *testing.T) {
	m := fixture()
	manager := &fakeTerminator{}
	m.manager = manager
	m.scanning = true // suppress refresh command in result
	m.cursor = 1
	m.askStop()
	m, cmd := key(m, "y")
	if cmd == nil || !m.stopping || manager.calls != 0 {
		t.Fatal("signal should be asynchronous")
	}
	u, _ := m.Update(cmd())
	m = u.(Model)
	if manager.calls != 1 || manager.force || m.modal == nil || !m.modal.force {
		t.Fatal("expected separate force prompt")
	}
	m, _ = key(m, "enter")
	if manager.calls != 1 || m.modal != nil {
		t.Fatal("force must default to cancel")
	}
}

func TestScanFailureKeepsSnapshotAndToggleQueues(t *testing.T) {
	m := fixture()
	u, _ := m.Update(scanMsg{err: errors.New("failed")})
	m = u.(Model)
	if len(m.snapshot.Processes) != 3 || m.lastError == "" {
		t.Fatal("failed refresh erased known data")
	}
	m.scanning = true
	m, _ = key(m, "a")
	if !m.scanAgain || !m.all {
		t.Fatal("toggle during scan must queue fresh scan")
	}
}

func TestSmallTerminalCannotConfirmHiddenDialog(t *testing.T) {
	m := fixture()
	m.askStop()
	m.width = 40
	m, cmd := key(m, "y")
	if cmd != nil || m.stopping || m.modal == nil {
		t.Fatal("a hidden dialog must not accept confirmation")
	}
}

func TestSearchCancelAndResetFilters(t *testing.T) {
	m := fixture()
	m.query = "node"
	m.rebuild()
	m, _ = key(m, "/")
	m, _ = key(m, "8080")
	m, _ = key(m, "esc")
	if m.query != "node" || m.searching {
		t.Fatal("Escape must restore the previous search")
	}
	m.category = 5
	m.rebuild()
	m, _ = key(m, "esc")
	if m.query != "" || m.category != 0 || len(m.rows) == 0 {
		t.Fatal("Escape outside search must reset filters")
	}
}

func TestRightArrowExpandsAndEntersGroup(t *testing.T) {
	m := fixture()
	group := m.rows[0].key
	m.collapsed[group] = true
	m.rebuild()
	m.expandGroup()
	if m.collapsed[group] || m.cursor != 0 {
		t.Fatal("first right arrow expands")
	}
	m.expandGroup()
	if m.cursor != 1 || m.collapsed[group] {
		t.Fatal("second right arrow enters the group")
	}
}

func TestContextActionsAndHiddenSelections(t *testing.T) {
	m := fixture()
	m.toggleSelection()
	m.category = 5
	m.rebuild()
	_, hidden := m.selectionCounts()
	if hidden != 2 {
		t.Fatal("hidden selection must be explicit")
	}
	if !strings.Contains(ansi.Strip(m.View()), "2 hidden by filters") {
		t.Fatal("selection warning missing")
	}
	for _, a := range m.actions() {
		if a.key == "d" && (a.disabled || a.label != "Stop 2") {
			t.Fatal("hidden selections remain explicit stop targets")
		}
	}
	m.selected = map[string]bool{}
	for _, a := range m.actions() {
		if (a.key == "d" || a.key == "Space") && !a.disabled {
			t.Fatal("protected rows must disable actions")
		}
	}
}

func TestDiscoveryWarningsDoNotHideSelection(t *testing.T) {
	for _, failed := range []bool{false, true} {
		m := fixture()
		m.width = 160
		m.snapshot.Warnings = []string{"Limited /proc access: other users' ports may be hidden"}
		if failed {
			m.lastError = "permission denied"
		}
		u, _ := m.Update(tea.MouseMsg{X: 4, Y: 9, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		m = u.(Model)
		for _, hidden := range []bool{false, true} {
			if hidden {
				m.category = 5
				m.rebuild()
			}
			view := ansi.Strip(m.View())
			if !strings.Contains(view, "1 process selected") {
				t.Fatalf("warning hid the selected process (failed=%v hidden=%v)\n%s", failed, hidden, view)
			}
			if hidden && !strings.Contains(view, "1 hidden by filters") {
				t.Fatal("warning hid a selected process outside the current filter")
			}
			issue := "Limited /proc access"
			if failed {
				issue = "Refresh failed; showing last snapshot: permission denied"
			}
			if !strings.Contains(view, issue) {
				t.Fatal("selection hid the discovery warning")
			}
		}
		m, _ = key(m, "esc")
		if strings.Contains(ansi.Strip(m.View()), "process selected") {
			t.Fatal("cleared selection is still displayed")
		}
	}
}

func TestFooterBoundsAndInertWhitespace(t *testing.T) {
	for _, width := range []int{64, 80, 95, 110, 140} {
		m := fixture()
		m.width = width
		line := ansi.Strip(m.footerView())
		found := map[string]bool{}
		for _, a := range m.actions() {
			found[a.key] = true
			if a.end > width || !strings.HasPrefix(line[a.start:], a.text()) {
				t.Fatalf("incorrect %s button bounds at %d", a.key, width)
			}
		}
		for _, k := range []string{"Enter", "Space", "d", "/", "?"} {
			if !found[k] {
				t.Fatalf("missing primary action %s at %d", k, width)
			}
		}
		u, cmd := m.Update(tea.MouseMsg{X: 0, Y: m.height - 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if u.(Model).modal != nil || cmd != nil {
			t.Fatal("footer padding must not trigger an action")
		}
	}
}

func TestMixedGroupSelectionAndManyTargets(t *testing.T) {
	m := fixture()
	members := append([]model.Process{}, m.snapshot.Processes...)
	m.selected[members[0].Key()] = true
	m.selected[members[1].Key()] = true
	if m.groupCheck(members) != "[✓]" {
		t.Fatal("protected members must not prevent complete selection")
	}
	m.modal = &dialog{}
	for i := 0; i < 30; i++ {
		m.modal.targets = append(m.modal.targets, members[0])
	}
	m.height = 22
	m.width = 64
	if len(strings.Split(m.View(), "\n")) != 22 {
		t.Fatal("large confirmation must fit a small terminal")
	}
}
