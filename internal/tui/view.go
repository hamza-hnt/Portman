package tui

import (
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"portman/internal/model"
)

var (
	accent        = lipgloss.NewStyle().Foreground(lipgloss.Color("#70E1C3"))
	muted         = lipgloss.NewStyle().Foreground(lipgloss.Color("#858CA5"))
	bright        = lipgloss.NewStyle().Foreground(lipgloss.Color("#E4E7F2"))
	violet        = lipgloss.NewStyle().Foreground(lipgloss.Color("#B6A0F5"))
	warning       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F4C77E"))
	danger        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8495"))
	rule          = lipgloss.NewStyle().Foreground(lipgloss.Color("#344455"))
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#1D3539")).Foreground(lipgloss.Color("#F0F4FA"))
	tabLabels     = []string{"0 All", "1 Dev", "2 Brew", "3 Docker", "4 Apps", "5 System"}
)

// Process names, paths, and project manifests are untrusted terminal content.
// Never allow escape sequences or controls to execute in the user's terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, s)
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
func field(s string, w int) string { return fit(clean(s), w) }

func (m Model) View() string {
	if m.width < 64 || m.height < 22 {
		return ansi.Truncate("Portman · resize terminal to at least 64 × 22 · q to quit", max(1, m.width), "…")
	}
	if m.modal != nil {
		return m.dialogView()
	}
	if m.help || m.details {
		return m.documentView()
	}
	lines := m.headerLines()
	for i := 0; i < m.bodyHeight(); i++ {
		index := m.offset + i
		if index < len(m.rows) {
			lines = append(lines, m.rowView(m.rows[index], index == m.cursor))
			continue
		}
		line := ""
		if len(m.rows) == 0 {
			switch i {
			case 1:
				text := "No listening ports found."
				if m.query != "" || m.category != 0 {
					text = "No processes match your filters."
				}
				if m.scanning && m.snapshot.At.IsZero() {
					text = "Discovering your local workspace…"
				}
				line = bright.Render("    " + text)
			case 2:
				text := "[a] Show all connections    [r] Refresh"
				if m.query != "" || m.category != 0 {
					text = "[Esc] Reset filters    [/] Edit search"
				}
				line = muted.Render("    " + text)
			}
		}
		lines = append(lines, line)
	}
	lines = append(lines, rule.Render(strings.Repeat("─", m.width)))
	lines = append(lines, m.detailLines()...)
	updated := "Discovering…"
	if !m.snapshot.At.IsZero() {
		updated = "Updated " + m.snapshot.At.Local().Format("15:04:05")
	}
	if m.paused {
		updated = "Refresh paused · [p] resume"
	}
	matching := fmt.Sprintf("%d of %d processes", m.matchedCount(), len(m.snapshot.Processes))
	if len(m.rows) > m.bodyHeight() {
		matching = fmt.Sprintf("↑↓ %d–%d of %d rows · %d matches", m.offset+1, min(len(m.rows), m.offset+m.bodyHeight()), len(m.rows), m.matchedCount())
	}
	if m.scanning {
		matching += " · refreshing"
	}
	lines = append(lines, muted.Render(" "+fit(updated, m.width-ansi.StringWidth(matching)-3)+matching))
	status, style := m.status, muted
	if status == "" || strings.HasPrefix(status, "Ready") {
		status = "Click a row to focus · click its checkbox to select"
	}
	if len(m.selected) > 0 && !m.stopping {
		_, hidden := m.selectionCounts()
		status = plural(len(m.selected), "process") + " selected"
		if hidden > 0 {
			status += fmt.Sprintf(" · %d hidden by filters", hidden)
		}
		status += " · [Esc] clear selection"
		style = accent
	}
	if m.lastError != "" {
		status = "Refresh failed; showing last snapshot: " + m.lastError
		style = danger
	} else if len(m.snapshot.Warnings) > 0 {
		status = m.snapshot.Warnings[0]
		style = warning
	}
	lines = append(lines, style.Render(" "+clean(status)))
	lines = append(lines, rule.Render(strings.Repeat("─", m.width)), m.footerView())
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) headerLines() []string {
	state := accent.Bold(true).Render("● LIVE")
	if m.paused {
		state = warning.Bold(true).Render("Ⅱ PAUSED")
	}
	if m.snapshot.At.IsZero() {
		state = muted.Render("◌ STARTING")
	}
	if m.lastError != "" {
		state = warning.Render("! STALE")
	}
	osName := "Linux"
	if runtime.GOOS == "darwin" {
		osName = "macOS"
	}
	right := muted.Render(osName+"  ") + state
	mark := accent.Render(" [") + violet.Bold(true).Render(">_") + accent.Render("]  ")
	brand := mark + bright.Bold(true).Render("PORTMAN") + muted.Render("  /  local workspace")
	line0 := fit(brand, m.width-ansi.StringWidth(right)-1) + right
	mode := "[a] Listening ports"
	if m.all {
		mode = "[a] All connections"
	}
	subtitle := muted.Render("       Local ports. Clear ownership.")
	line1 := fit(subtitle, m.width-ansi.StringWidth(mode)-1) + muted.Render(mode)
	ports, projects := 0, map[string]bool{}
	for _, p := range m.snapshot.Processes {
		ports += len(p.Sockets)
		if p.Project != nil && p.Category == model.Development {
			projects[p.Project.Root] = true
		}
	}
	metric := func(n int, noun string) string {
		label := strings.TrimPrefix(plural(n, noun), strconv.Itoa(n)+" ")
		return bright.Bold(true).Render(strconv.Itoa(n)) + muted.Render(" "+label)
	}
	line2 := "       " + metric(len(m.snapshot.Processes), "process") + muted.Render("   ·   ") + metric(ports, "socket") + muted.Render("   ·   ") + metric(len(projects), "project")
	tabs := " "
	for i, item := range m.tabs() {
		style := muted
		if i == m.category {
			style = accent.Bold(true).Background(lipgloss.Color("#1D3539")).Underline(true)
		}
		tabs += style.Render(item.label) + " "
	}
	search := muted.Render(" [/] Search by port, name, project or PID")
	hint := "↑↓ navigate"
	if m.query != "" || m.searching {
		search = accent.Render(" [/] ") + bright.Render(clean(m.query))
		hint = fmt.Sprintf("%d matches · Esc reset", m.matchedCount())
	}
	if m.searching {
		search += accent.Render("▏")
		hint = "Enter apply · Esc cancel"
	}
	line5 := fit(search, m.width-ansi.StringWidth(hint)-2) + muted.Render(hint)
	c := m.columns()
	head := "         " + field("PROJECT / PROCESS", c.name) + " " + field("PID", c.pid) + "  " + field("PORTS", c.ports)
	if c.address > 0 {
		head += "  " + field("BINDING", c.address)
	}
	head += "  " + field("STATE", c.state)
	return []string{line0, line1, line2, "", tabs, line5, muted.Render(head), rule.Render(strings.Repeat("─", m.width))}
}

func socketSummary(sockets []model.Socket) string {
	seen := map[string]bool{}
	labels := []string{}
	for _, s := range sockets {
		label := strconv.Itoa(s.Port) + "/" + strings.ToLower(s.Protocol)
		if !seen[label] {
			labels = append(labels, label)
			seen[label] = true
		}
	}
	return strings.Join(labels, ", ")
}

func (m Model) rowView(r row, focused bool) string {
	pointer := " "
	if focused {
		pointer = "›"
	}
	prefix := " " + pointer + " "
	var text string
	if r.process == nil {
		caret := "▾"
		if m.collapsed[r.key] {
			caret = "▸"
		}
		label := prefix + m.groupCheck(r.members) + " " + caret + " " + clean(r.label)
		count := plural(len(r.members), "process")
		locked := 0
		for _, p := range r.members {
			if p.Protected != "" {
				locked++
			}
		}
		if locked > 0 {
			count += fmt.Sprintf(" · %d protected", locked)
		}
		right := " " + count + " "
		text = fit(violet.Bold(true).Render(label), m.width-ansi.StringWidth(right)) + muted.Render(right)
	} else {
		p := *r.process
		c := m.columns()
		check := "[ ]"
		if m.selected[p.Key()] {
			check = "[✓]"
		}
		if p.Protected != "" {
			check = "[-]"
		}
		branch := "├"
		if r.last {
			branch = "└"
		}
		nameStyle := bright
		if p.Protected != "" {
			nameStyle = muted
		}
		text = accent.Render(prefix) + muted.Render(check+" "+branch+" ") + nameStyle.Render(field(p.Name, c.name)) + " " + muted.Render(field(strconv.Itoa(p.PID), c.pid)) + "  " + accent.Render(field(socketSummary(p.Sockets), c.ports))
		if c.address > 0 {
			text += "  " + muted.Render(field(bindings(p), c.address))
		}
		state := processState(p)
		style := accent
		if p.Protected != "" {
			style = muted
		}
		text += "  " + style.Render(field(state, c.state))
	}
	if focused {
		// Nested segment resets would otherwise interrupt the selection band.
		return selectedStyle.Render(fit(ansi.Strip(text), m.width))
	}
	return text
}

func (m Model) footerView() string {
	line := " "
	for _, item := range m.actions() {
		style := bright
		if item.key == "d" {
			style = danger
		}
		if item.disabled {
			style = muted
		}
		line += style.Render(item.text()) + "  "
	}
	return line
}

// Five fixed detail lines keep table/mouse geometry stable across refreshes.
func (m Model) detailLines() []string {
	lines := []string{muted.Render(" INSPECTOR"), "", muted.Render(" Choose a process to see its ports and project."), "", ""}
	if len(m.rows) == 0 {
		return lines
	}
	r := m.rows[m.cursor]
	if r.process == nil {
		label := "GROUP"
		if r.project != nil {
			label = "PROJECT"
		}
		lines[0] = muted.Render(" "+label+"  ") + violet.Bold(true).Render(clean(r.label))
		if r.project != nil {
			lines[1] = muted.Render(" Folder   ") + bright.Render(clean(r.project.Root))
		} else {
			lines[1] = muted.Render(" " + categoryDescription(r.members[0].Category))
		}
		names := []string{}
		eligible := 0
		for _, p := range r.members {
			names = append(names, p.Name)
			if p.Protected == "" {
				eligible++
			}
		}
		lines[2] = muted.Render(" Services ") + bright.Render(clean(strings.Join(names, " · ")))
		lines[3] = muted.Render(" " + plural(len(r.members), "matching process") + " · " + plural(eligible, "stoppable process"))
		if eligible > 0 {
			lines[4] = accent.Render(" Space selects this group · d reviews the processes to stop")
		} else {
			lines[4] = warning.Render(" Protected group · inspection available, termination disabled")
		}
		return lines
	}
	p := r.process
	lines[0] = muted.Render(" PROCESS  ") + accent.Bold(true).Render(clean(p.Name)) + muted.Render(fmt.Sprintf("  ·  PID %d  ·  %s", p.PID, p.Category))
	lines[1] = muted.Render(" Binary   ") + bright.Render(clean(p.Executable))
	lines[2] = muted.Render(" Folder   ") + bright.Render(clean(p.CWD))
	if len(p.Sockets) > 0 {
		s := p.Sockets[0]
		lines[3] = muted.Render(" Address  ") + accent.Render(clean(net.JoinHostPort(s.Address, strconv.Itoa(s.Port)))) + muted.Render(fmt.Sprintf(" · %s · %s · %s", s.Protocol, s.State, plural(len(p.Sockets), "socket")))
	}
	if p.Protected != "" {
		lines[4] = warning.Render(" Protected · " + clean(p.Protected))
	} else if p.Project != nil {
		lines[4] = violet.Render(" Project  "+clean(p.Project.Name)) + muted.Render(" · Enter to inspect all details")
	} else {
		lines[4] = muted.Render(" Enter shows all sockets · d opens a confirmation before stopping")
	}
	return lines
}

func categoryDescription(c model.Category) string {
	switch c {
	case model.Development:
		return "Local services grouped by their project folder."
	case model.Homebrew:
		return "Background services installed with Homebrew."
	case model.Docker:
		return "Docker host processes. Stopping the backend can affect every container."
	case model.System:
		return "System services. Protected to keep your system running."
	default:
		return "Apps and other local processes using network ports."
	}
}

func (m Model) dialogCapacity() int { return max(1, min(8, m.height-18)) }
func (m Model) dialogLines() []string {
	d := m.modal
	title := "Stop selected processes?"
	explanation := "Ask these processes to shut down cleanly (SIGTERM)."
	step := "01 / GRACEFUL STOP"
	if d.force {
		title = "Processes are still running"
		explanation = "SIGKILL stops immediately. Unsaved work may be lost."
		step = "02 / FORCE STOP"
	}
	lines := []string{muted.Render(step), warning.Bold(true).Render(title), "", bright.Render(clean(explanation)), muted.Render(plural(len(d.targets), "process") + " · only the list below will be affected"), ""}
	start := min(d.offset, max(0, len(d.targets)-m.dialogCapacity()))
	for _, p := range d.targets[start:min(len(d.targets), start+m.dialogCapacity())] {
		lines = append(lines, fmt.Sprintf("  %-7d %s  ·  %s", p.PID, clean(p.Name), socketSummary(p.Sockets)))
	}
	if len(d.targets) > m.dialogCapacity() {
		lines = append(lines, muted.Render(fmt.Sprintf("  ↑↓ scroll targets · %d–%d of %d", start+1, min(len(d.targets), start+m.dialogCapacity()), len(d.targets))))
	}
	lines = append(lines, "", muted.Render("Enter cancels by default · Tab switches buttons · Esc goes back"))
	docker := false
	for _, p := range d.targets {
		if p.Category == model.Docker {
			docker = true
		}
	}
	if docker {
		lines = append(lines, warning.Render("Stopping Docker’s backend may affect every local container."))
	} else {
		lines = append(lines, muted.Render("Supervisors may restart services after they exit."))
	}
	lines = append(lines, "")
	cancel, confirm := "[n] Cancel   ", "[y] Send SIGTERM    "
	if d.force {
		confirm = "[y] Send SIGKILL    "
	}
	if d.confirm {
		confirm = danger.Bold(true).Reverse(true).Render(confirm)
	} else {
		cancel = accent.Reverse(true).Render(cancel)
	}
	lines = append(lines, cancel+"  "+confirm)
	return lines
}

func (m Model) dialogGeometry() (int, int, int, int) {
	w := min(76, m.width-4)
	h := len(m.dialogLines()) + 4
	return (m.width - w) / 2, (m.height - h) / 2, w, len(m.dialogLines()) + 1
}

func (m Model) dialogView() string {
	_, _, w, _ := m.dialogGeometry()
	lines := m.dialogLines()
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], w-6, "…")
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#B6A0F5")).Padding(1, 2).Width(w - 2).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) documentView() string {
	title := "PORTMAN / INSPECT"
	lines := []string{}
	if m.help {
		title = "PORTMAN / KEYBOARD & MOUSE"
		lines = []string{"↑/↓ or j/k     Move through projects and processes", "Enter          Inspect process or toggle a group", "← / →          Collapse group / expand and enter group", "Space          Select process / matching group", "d or x         Stop selected processes, or focused row/group", "/              Search live; Enter applies, Esc cancels editing", "0–5 / Tab      Switch categories", "a              Toggle all TCP connections / listening TCP", "p / r          Pause automatic refresh / refresh now", "i              Inspect all sockets and last termination results", "Esc            Reset search, category and selection", "q / Ctrl+C     Quit (Esc or q closes dialogs first)", "", "Click a row to focus; click a group to expand/collapse.", "Click the checkbox area to select; wheel scrolls the list.", "Category tabs, search, footer actions and dialog buttons are clickable.", "", "Safety", "SIGTERM first; surviving processes require a separate SIGKILL confirmation.", "Root, system, other users, and Portman’s terminal ancestry are protected.", "Group actions include matching discovered processes, not arbitrary children.", "Selections hidden by a filter remain selected; see the confirmation list.", "Docker entries represent host processes, not individual containers.", "Visibility is limited by OS permissions. Running as root is unnecessary.", "", "Search uses case-insensitive AND matching across words.", "TCP listeners and bound UDP sockets are shown by default."}
	} else {
		for _, p := range m.rowTargets() {
			lines = append(lines, fmt.Sprintf("%s · PID %d · PPID %d · UID %d", p.Name, p.PID, p.PPID, p.UID), "Category   "+string(p.Category), "Executable "+p.Executable, "Directory  "+p.CWD, fmt.Sprintf("Identity   %s", p.Key()))
			if p.Project != nil {
				lines = append(lines, "Project    "+p.Project.Name, "Root       "+p.Project.Root, "Evidence   "+p.Project.Marker+" / "+p.Project.Source)
			}
			if p.Protected != "" {
				lines = append(lines, "Protected  "+p.Protected)
			}
			for _, s := range p.Sockets {
				entry := fmt.Sprintf("  %-4s %-5s %-28s %s", s.Protocol, s.Family, net.JoinHostPort(s.Address, strconv.Itoa(s.Port)), s.State)
				if s.Remote != "" {
					entry += " → " + s.Remote
				}
				lines = append(lines, entry)
			}
			lines = append(lines, "")
		}
		if len(m.lastResults) > 0 {
			lines = append(lines, "LAST TERMINATION")
			for _, r := range m.lastResults {
				lines = append(lines, fmt.Sprintf("PID %d %s · %s · %s", r.Process.PID, r.Process.Name, r.Status, r.Detail))
			}
		}
	}
	wrapped := []string{}
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(clean(line), m.width-4, ""), "\n")...)
	}
	capacity := m.height - 4
	offset := min(m.detailOffset, max(0, len(wrapped)-capacity))
	out := []string{accent.Bold(true).Render(" " + title), muted.Render(strings.Repeat("─", m.width))}
	for i := 0; i < capacity; i++ {
		line := ""
		if offset+i < len(wrapped) {
			line = "  " + wrapped[offset+i]
		}
		out = append(out, fit(bright.Render(line), m.width))
	}
	out = append(out, muted.Render(strings.Repeat("─", m.width)), muted.Render(" ↑↓ / wheel scroll · Esc close"))
	return strings.Join(out, "\n")
}
