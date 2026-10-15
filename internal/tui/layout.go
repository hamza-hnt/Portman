package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"portman/internal/model"
)

const tableTop = 8

func workloadNoun(items []model.Process) string {
	containers := 0
	for _, p := range items {
		if p.Container != nil {
			containers++
		}
	}
	if containers == 0 {
		return "process"
	}
	if containers == len(items) {
		return "container"
	}
	return "workload"
}

type tableColumns struct{ name, pid, ports, address, state int }

func (m Model) columns() tableColumns {
	if m.width >= 120 {
		return tableColumns{m.width - 89, 7, 25, 22, 19}
	}
	if m.width >= 95 {
		return tableColumns{m.width - 64, 7, 25, 0, 18}
	}
	return tableColumns{m.width - 44, 6, 16, 0, 8}
}

type action struct {
	key, label string
	start, end int
	disabled   bool
}

func (a action) text() string { return "[" + a.key + "] " + a.label }

// Rendering and hit testing share these exact bounds. No invisible actions in
// the padding or truncated buttons, including after a terminal resize.
func (m Model) actions() []action {
	enter, stop := "Inspect", "Stop"
	eligible := 0
	for _, p := range m.stopTargets() {
		if p.Protected == "" {
			eligible++
		}
	}
	if len(m.rows) > 0 && m.rows[m.cursor].expandable() {
		enter = "Collapse"
		if m.collapsed[m.rows[m.cursor].key] {
			enter = "Expand"
		}
		if m.width >= 95 && m.rows[m.cursor].process == nil {
			stop = "Stop group"
		}
	}
	if len(m.rows) > 0 && m.rows[m.cursor].process != nil && m.rows[m.cursor].process.Container != nil && m.width >= 95 {
		stop = "Stop container"
	}
	if len(m.selected) > 0 {
		stop = fmt.Sprintf("Stop %d", eligible)
	}
	if m.width < 80 {
		enter = "Open"
		if len(m.rows) > 0 && m.rows[m.cursor].expandable() && !m.collapsed[m.rows[m.cursor].key] {
			enter = "Fold"
		}
	}
	if eligible == 0 {
		stop = "Protected"
		if len(m.rows) == 0 {
			stop = "Stop"
		}
	}
	if m.stopping {
		stop = "Stopping"
	}
	if m.width < 95 && eligible == 0 {
		stop = "Locked"
	}
	rowEligible := 0
	for _, p := range m.rowTargets() {
		if p.Protected == "" {
			rowEligible++
		}
	}
	items := []action{{key: "Enter", label: enter, disabled: len(m.rows) == 0}, {key: "Space", label: "Select", disabled: rowEligible == 0}, {key: "d", label: stop, disabled: eligible == 0 || m.stopping}, {key: "/", label: "Search"}}
	if m.width >= 95 {
		items = append(items, action{key: "r", label: "Refresh"})
	}
	if m.width >= 110 {
		label := "Pause"
		if m.paused {
			label = "Resume"
		}
		items = append(items, action{key: "p", label: label})
	}
	items = append(items, action{key: "?", label: "Help"})
	if m.width >= 120 {
		items = append(items, action{key: "q", label: "Quit"})
	}
	result := []action{}
	x := 1
	for _, item := range items {
		w := ansi.StringWidth(item.text())
		if x+w > m.width {
			continue
		}
		item.start = x
		item.end = x + w
		result = append(result, item)
		x += w + 2
	}
	return result
}

func (m Model) stopTargets() []model.Process {
	if len(m.selected) == 0 {
		return m.rowTargets()
	}
	targets := []model.Process{}
	for _, p := range m.snapshot.Processes {
		if m.selected[p.Key()] {
			targets = append(targets, p)
		}
	}
	return targets
}

func (m Model) selectionCounts() (shown, hidden int) {
	visible := map[string]bool{}
	for _, r := range m.rows {
		for _, p := range r.members {
			visible[p.Key()] = true
		}
	}
	for key := range m.selected {
		if visible[key] {
			shown++
		} else {
			hidden++
		}
	}
	return
}

func (m Model) matchedCount() int {
	n := 0
	for _, r := range m.rows {
		if r.process == nil && r.parent == "" {
			n += len(r.members)
		}
	}
	return n
}

func (m Model) tabs() []action {
	counts := make([]int, 6)
	for _, p := range m.snapshot.Processes {
		counts[0]++
		for i, c := range model.Categories {
			if p.Category == c {
				counts[i+1]++
			}
		}
	}
	items := []action{}
	x := 1
	for i, label := range tabLabels {
		if m.width >= 95 {
			label += fmt.Sprintf(" %d", counts[i])
		}
		label = " " + label + " "
		items = append(items, action{label: label, start: x, end: x + ansi.StringWidth(label)})
		x += ansi.StringWidth(label) + 1
	}
	return items
}

func (m Model) groupCheck(members []model.Process) string {
	eligible, selected := 0, 0
	for _, p := range members {
		if p.Protected == "" {
			eligible++
			if m.selected[p.Key()] {
				selected++
			}
		}
	}
	if eligible == 0 {
		return "[-]"
	}
	if selected == eligible {
		return "[✓]"
	}
	if selected > 0 {
		return "[−]"
	}
	return "[ ]"
}

func processState(p model.Process) string {
	if p.Protected != "" {
		return "GUARDED"
	}
	if p.Container != nil {
		return "RUNNING"
	}
	for _, s := range p.Sockets {
		if s.State == "LISTEN" {
			return "LISTEN"
		}
	}
	for _, s := range p.Sockets {
		if s.Protocol == "TCP" {
			return "ACTIVE"
		}
	}
	return "BOUND"
}

func bindings(p model.Process) string {
	seen := map[string]bool{}
	labels := []string{}
	for _, s := range p.Sockets {
		label := s.Address
		if label == "*" || label == "0.0.0.0" || label == "::" {
			label = "all interfaces"
		}
		if !seen[label] {
			labels = append(labels, label)
			seen[label] = true
		}
	}
	return strings.Join(labels, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	if strings.HasSuffix(noun, "process") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
