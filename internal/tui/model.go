// Package tui is the Bubble Tea application. All mutable UI state lives here;
// discovery and termination run as commands and return immutable messages.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"portman/internal/model"
	"portman/internal/process"
)

type Scanner interface {
	Scan(context.Context, bool) (model.Snapshot, error)
}
type Terminator interface {
	Terminate(context.Context, []model.Process, bool) []process.Result
}

type scanMsg struct {
	snapshot model.Snapshot
	err      error
	all      bool
}
type tickMsg time.Time
type stopMsg struct {
	results []process.Result
	force   bool
}

type row struct {
	key     string
	label   string
	project *model.Project
	process *model.Process
	members []model.Process
	last    bool
}

type dialog struct {
	targets []model.Process
	force   bool
	confirm bool // default focus is Cancel
	offset  int
}

type Model struct {
	scanner                         Scanner
	manager                         Terminator
	interval                        time.Duration
	width, height                   int
	snapshot                        model.Snapshot
	rows                            []row
	cursor, offset                  int
	query                           string
	searchBefore                    string
	searching                       bool
	category                        int
	collapsed                       map[string]bool
	selected                        map[string]bool
	all, paused, scanning, stopping bool
	scanAgain                       bool
	modal                           *dialog
	help, details                   bool
	detailOffset                    int
	status, lastError               string
	lastResults                     []process.Result
	ctx                             context.Context
}

func New(ctx context.Context, scanner Scanner, manager Terminator, interval time.Duration, all bool) Model {
	return Model{ctx: ctx, scanner: scanner, manager: manager, interval: interval, all: all, collapsed: map[string]bool{}, selected: map[string]bool{}, width: 100, height: 30, scanning: true, status: "Discovering local sockets…"}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.scan(), m.tick()) }
func (m Model) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m Model) scan() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		s, err := m.scanner.Scan(ctx, m.all)
		return scanMsg{s, err, m.all}
	}
}

func (m *Model) refresh() tea.Cmd {
	if m.scanning {
		m.scanAgain = true
		return nil
	}
	m.scanning = true
	return m.scan()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureVisible()
	case tickMsg:
		var cmd tea.Cmd
		if !m.paused && !m.scanning {
			cmd = m.refresh()
		}
		return m, tea.Batch(cmd, m.tick())
	case scanMsg:
		m.scanning = false
		if msg.err != nil {
			m.lastError = msg.err.Error()
		} else if msg.all == m.all {
			m.snapshot = msg.snapshot
			m.lastError = ""
			m.rebuild()
			if m.status == "Discovering local sockets…" {
				m.status = "Ready · listening TCP + bound UDP"
			}
		}
		if m.scanAgain || msg.all != m.all {
			m.scanAgain = false
			return m, m.refresh()
		}
	case stopMsg:
		m.stopping = false
		m.lastResults = msg.results
		var survivors []model.Process
		exited, failed := 0, 0
		for _, r := range msg.results {
			delete(m.selected, r.Process.Key())
			switch r.Status {
			case "exited":
				exited++
			case "running":
				survivors = append(survivors, r.Process)
			default:
				failed++
			}
		}
		m.status = fmt.Sprintf("%d exited · %d still running · %d refused/failed · i shows results", exited, len(survivors), failed)
		if len(survivors) > 0 && !msg.force {
			m.modal = &dialog{targets: survivors, force: true}
		}
		return m, m.refresh()
	case tea.MouseMsg:
		if m.width < 64 || m.height < 22 {
			return m, nil
		}
		return m.mouse(msg)
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.width < 64 || m.height < 22 {
			if key == "q" {
				return m, tea.Quit
			}
			return m, nil
		}
		if m.modal != nil {
			return m.dialogKey(key)
		}
		if m.help || m.details {
			switch key {
			case "q", "esc", "i", "?":
				m.help = false
				m.details = false
				m.detailOffset = 0
			case "j", "down":
				m.detailOffset++
			case "k", "up":
				m.detailOffset = max(0, m.detailOffset-1)
			}
			return m, nil
		}
		if m.searching {
			switch key {
			case "esc":
				m.query = m.searchBefore
				m.searching = false
			case "enter":
				m.searching = false
			case "backspace", "ctrl+h":
				chars := []rune(m.query)
				if len(chars) > 0 {
					m.query = string(chars[:len(chars)-1])
				}
			case "ctrl+u":
				m.query = ""
			default:
				if msg.Type == tea.KeyRunes {
					runes := []rune(m.query + clean(string(msg.Runes)))
					m.query = string(runes[:min(200, len(runes))])
				}
				if msg.Type == tea.KeySpace && len([]rune(m.query)) < 200 {
					m.query += " "
				}
			}
			m.rebuild()
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "j", "down":
			m.move(1)
		case "k", "up":
			m.move(-1)
		case "pgdown", "ctrl+d":
			m.move(m.bodyHeight())
		case "pgup", "ctrl+u":
			m.move(-m.bodyHeight())
		case "g", "home":
			m.cursor = 0
			m.ensureVisible()
		case "G", "end":
			m.cursor = max(0, len(m.rows)-1)
			m.ensureVisible()
		case "/":
			m.beginSearch()
		case "esc":
			m.query = ""
			m.category = 0
			m.selected = map[string]bool{}
			m.rebuild()
		case "tab":
			m.category = (m.category + 1) % 6
			m.rebuild()
		case "shift+tab":
			m.category = (m.category + 5) % 6
			m.rebuild()
		case "0", "1", "2", "3", "4", "5":
			m.category, _ = strconv.Atoi(key)
			m.rebuild()
		case " ":
			m.toggleSelection()
		case "enter":
			m.activateRow()
		case "right", "l":
			m.expandGroup()
		case "left", "h":
			m.toggleGroup(true)
		case "d", "x":
			m.askStop()
		case "r":
			return m, m.refresh()
		case "p":
			m.paused = !m.paused
		case "a":
			m.all = !m.all
			return m, m.refresh()
		case "?":
			m.help = true
		case "i":
			m.details = true
			m.detailOffset = 0
		}
	}
	return m, nil
}

func (m *Model) move(n int)     { m.cursor = max(0, min(len(m.rows)-1, m.cursor+n)); m.ensureVisible() }
func (m Model) bodyHeight() int { return max(1, m.height-18) }
func (m *Model) ensureVisible() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.bodyHeight() {
		m.offset = m.cursor - m.bodyHeight() + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.rows)-m.bodyHeight())))
}

func matches(p model.Process, query string) bool {
	parts := []string{p.Name, p.Executable, p.CWD, string(p.Category), strconv.Itoa(p.PID)}
	if p.Project != nil {
		parts = append(parts, p.Project.Name, p.Project.Root)
	}
	for _, s := range p.Sockets {
		parts = append(parts, strconv.Itoa(s.Port), s.Address, s.Protocol, s.State)
	}
	haystack := strings.ToLower(strings.Join(parts, " "))
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(haystack, word) {
			return false
		}
	}
	return true
}

func (m *Model) rebuild() {
	focused := ""
	if m.cursor < len(m.rows) {
		focused = m.rows[m.cursor].key
	}
	groups := map[string]*row{}
	live := map[string]bool{}
	for _, p := range m.snapshot.Processes {
		live[p.Key()] = true
		if m.category > 0 && p.Category != model.Categories[m.category-1] {
			continue
		}
		if !matches(p, m.query) {
			continue
		}
		key, label := "category:"+string(p.Category), string(p.Category)
		if p.Project != nil && p.Category == model.Development {
			key = "project:" + p.Project.Root
			label = p.Project.Name
		}
		g := groups[key]
		if g == nil {
			g = &row{key: key, label: label, project: p.Project}
			groups[key] = g
		}
		g.members = append(g.members, p)
	}
	for key := range m.selected {
		if !live[key] {
			delete(m.selected, key)
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := groups[keys[i]], groups[keys[j]]
		rank := func(c model.Category) int {
			for i, v := range model.Categories {
				if v == c {
					return i
				}
			}
			return 9
		}
		ar, br := rank(a.members[0].Category), rank(b.members[0].Category)
		if ar != br {
			return ar < br
		}
		return keys[i] < keys[j]
	})
	m.rows = nil
	for _, key := range keys {
		g := groups[key]
		sort.Slice(g.members, func(i, j int) bool {
			a, b := g.members[i], g.members[j]
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.PID < b.PID
		})
		m.rows = append(m.rows, *g)
		if m.collapsed[key] {
			continue
		}
		for i, p := range g.members {
			p := p
			m.rows = append(m.rows, row{key: p.Key(), process: &p, last: i == len(g.members)-1})
		}
	}
	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
	for i, r := range m.rows {
		if r.key == focused {
			m.cursor = i
			break
		}
	}
	m.ensureVisible()
}

func (m *Model) toggleGroup(collapse bool) {
	if len(m.rows) == 0 {
		return
	}
	r := m.rows[m.cursor]
	if r.process == nil {
		if collapse {
			m.collapsed[r.key] = true
		} else {
			m.collapsed[r.key] = !m.collapsed[r.key]
		}
		m.rebuild()
		return
	}
	if collapse {
		for i := m.cursor - 1; i >= 0; i-- {
			if m.rows[i].process == nil {
				m.cursor = i
				m.collapsed[m.rows[i].key] = true
				m.rebuild()
				return
			}
		}
	} else {
		m.details = true
	}
}

func (m *Model) beginSearch() {
	if !m.searching {
		m.searchBefore = m.query
	}
	m.searching = true
}
func (m *Model) activateRow() { m.toggleGroup(false) }
func (m *Model) expandGroup() {
	if len(m.rows) == 0 {
		return
	}
	r := m.rows[m.cursor]
	if r.process != nil {
		m.details = true
		return
	}
	if m.collapsed[r.key] {
		m.collapsed[r.key] = false
		m.rebuild()
	} else if m.cursor+1 < len(m.rows) {
		m.move(1)
	}
}

func (m Model) rowTargets() []model.Process {
	if len(m.rows) == 0 {
		return nil
	}
	r := m.rows[m.cursor]
	if r.process != nil {
		return []model.Process{*r.process}
	}
	return r.members
}

func (m *Model) toggleSelection() {
	targets := m.rowTargets()
	all := true
	for _, p := range targets {
		if p.Protected == "" && !m.selected[p.Key()] {
			all = false
		}
	}
	for _, p := range targets {
		if p.Protected != "" {
			continue
		}
		if all {
			delete(m.selected, p.Key())
		} else {
			m.selected[p.Key()] = true
		}
	}
}

func (m *Model) askStop() {
	if m.stopping {
		return
	}
	targets := m.stopTargets()
	eligible := []model.Process{}
	protected := 0
	for _, p := range targets {
		if p.Protected != "" {
			protected++
			continue
		}
		eligible = append(eligible, p)
	}
	if len(eligible) == 0 {
		m.status = "No eligible processes · protected entries cannot be stopped"
		return
	}
	m.status = fmt.Sprintf("%d targets · %d protected entries excluded", len(eligible), protected)
	m.modal = &dialog{targets: eligible}
}

func (m Model) dialogKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "n", "q":
		m.modal = nil
	case "tab", "left", "right", "h", "l":
		m.modal.confirm = !m.modal.confirm
	case "down", "j":
		m.modal.offset = min(max(0, len(m.modal.targets)-m.dialogCapacity()), m.modal.offset+1)
	case "up", "k":
		m.modal.offset = max(0, m.modal.offset-1)
	case "enter":
		if !m.modal.confirm {
			m.modal = nil
		} else {
			return m.startStop()
		}
	case "y":
		return m.startStop()
	}
	return m, nil
}

func (m Model) startStop() (tea.Model, tea.Cmd) {
	targets := append([]model.Process(nil), m.modal.targets...)
	force := m.modal.force
	m.modal = nil
	m.stopping = true
	m.status = "Waiting for processes to exit…"
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		return stopMsg{m.manager.Terminate(ctx, targets, force), force}
	}
}

func (m Model) mouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action == tea.MouseActionRelease {
		return m, nil
	}
	if m.modal != nil {
		if msg.Button == tea.MouseButtonWheelDown {
			return m.dialogKey("down")
		}
		if msg.Button == tea.MouseButtonWheelUp {
			return m.dialogKey("up")
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			x, y, _, buttonY := m.dialogGeometry()
			if msg.Y == y+buttonY {
				if msg.X >= x+3 && msg.X < x+16 {
					m.modal = nil
				} else if msg.X >= x+18 && msg.X < x+37 {
					return m.startStop()
				}
			}
		}
		return m, nil
	}
	if m.help || m.details {
		if msg.Button == tea.MouseButtonWheelDown {
			m.detailOffset++
		}
		if msg.Button == tea.MouseButtonWheelUp {
			m.detailOffset = max(0, m.detailOffset-1)
		}
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelDown:
		m.move(3)
	case tea.MouseButtonWheelUp:
		m.move(-3)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		if msg.Y == 4 {
			for i, item := range m.tabs() {
				if msg.X >= item.start && msg.X < item.end {
					m.category = i
					m.rebuild()
					break
				}
			}
		} else if msg.Y == 5 {
			m.beginSearch()
		} else if msg.Y == 1 && msg.X >= m.width-21 {
			m.all = !m.all
			return m, m.refresh()
		} else if msg.Y >= tableTop && msg.Y < tableTop+m.bodyHeight() {
			index := m.offset + msg.Y - tableTop
			if index < len(m.rows) {
				m.cursor = index
				if msg.X >= 3 && msg.X < 6 {
					m.toggleSelection()
				} else if m.rows[index].process == nil {
					m.toggleGroup(false)
				}
			}
		} else if msg.Y == m.height-1 {
			for _, item := range m.actions() {
				if msg.X < item.start || msg.X >= item.end || item.disabled {
					continue
				}
				switch item.key {
				case "Enter":
					m.activateRow()
				case "Space":
					m.toggleSelection()
				case "d":
					m.askStop()
				case "/":
					m.beginSearch()
				case "r":
					return m, m.refresh()
				case "p":
					m.paused = !m.paused
				case "?":
					m.help = true
				case "q":
					return m, tea.Quit
				}
				break
			}
		}
	}
	return m, nil
}
