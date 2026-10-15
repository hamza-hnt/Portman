// Package discovery reads sockets from lsof's NUL-delimited machine format.
package discovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"portman/internal/model"
	"portman/internal/platform"
)

type Runner func(context.Context) ([]byte, []byte, error)

type Lsof struct {
	Inspector model.Inspector
	Run       Runner
}

func runLsof(ctx context.Context) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-iUDP", "-F0pcftPnT")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.Bytes(), err
}

func (l *Lsof) Scan(ctx context.Context, all bool) (model.Snapshot, error) {
	start := time.Now()
	out, stderr, err := l.Run(ctx)
	if ctx.Err() != nil {
		return model.Snapshot{}, ctx.Err()
	}
	// lsof uses exit 1 when there are no matching sockets. Other failures
	// must preserve the previous snapshot instead of silently erasing it.
	if err != nil {
		var e *exec.ExitError
		if !errors.As(err, &e) || e.ExitCode() != 1 || len(bytes.TrimSpace(stderr)) != 0 {
			return model.Snapshot{}, fmt.Errorf("lsof: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}
	s := model.Snapshot{At: start, Processes: []model.Process{}}
	if len(bytes.TrimSpace(stderr)) > 0 {
		s.Warnings = append(s.Warnings, strings.TrimSpace(string(stderr)))
	}
	missing := 0
	for _, raw := range Parse(out, all) {
		p, inspectErr := l.Inspector.Inspect(ctx, raw.PID)
		if errors.Is(inspectErr, platform.ErrGone) {
			continue
		}
		if inspectErr != nil {
			if ctx.Err() != nil {
				return model.Snapshot{}, ctx.Err()
			}
			// Visible sockets remain useful even when metadata is restricted.
			raw.UID = -1
			raw.Protected = "process identity unavailable"
			s.Processes = append(s.Processes, raw)
			missing++
			continue
		}
		if p.Zombie || p.StartID > uint64(start.UnixMicro()) {
			continue
		}
		p.Sockets = raw.Sockets
		if p.Name == "" {
			p.Name = raw.Name
		}
		s.Processes = append(s.Processes, p)
	}
	if missing > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("Metadata restricted for %d processes; termination disabled for those entries", missing))
	}
	sort.Slice(s.Processes, func(i, j int) bool { return s.Processes[i].PID < s.Processes[j].PID })
	s.Duration = time.Since(start)
	return s, nil
}

// Parse excludes unbound UDP sockets and (unless all) non-listening TCP.
func Parse(data []byte, all bool) []model.Process {
	byPID := map[int]*model.Process{}
	var p *model.Process
	var socket model.Socket
	hasFD := false
	flush := func() {
		if p == nil || !hasFD || socket.Port < 1 {
			return
		}
		if socket.Protocol != "TCP" && socket.Protocol != "UDP" {
			return
		}
		if socket.Protocol == "TCP" && !all && socket.State != "LISTEN" {
			return
		}
		if socket.Protocol == "UDP" {
			socket.State = "BOUND"
		}
		for _, existing := range p.Sockets {
			if existing == socket {
				return
			}
		}
		p.Sockets = append(p.Sockets, socket)
	}
	for _, field := range bytes.Split(data, []byte{0}) {
		field = bytes.TrimLeft(field, "\n")
		if len(field) == 0 {
			continue
		}
		value := string(field[1:])
		switch field[0] {
		case 'p':
			flush()
			hasFD = false
			socket = model.Socket{}
			p = nil
			pid, err := strconv.Atoi(value)
			if err == nil && pid > 0 {
				p = &model.Process{Identity: model.Identity{PID: pid}}
				byPID[pid] = p
			}
		case 'c':
			if p != nil {
				p.Name = value
			}
		case 'f':
			flush()
			socket = model.Socket{}
			hasFD = true
		case 't':
			socket.Family = value
		case 'P':
			socket.Protocol = value
		case 'T':
			if strings.HasPrefix(value, "ST=") {
				socket.State = strings.TrimPrefix(value, "ST=")
			}
		case 'n':
			local, remote, _ := strings.Cut(value, "->")
			socket.Remote = remote
			if i := strings.LastIndex(local, ":"); i >= 0 {
				socket.Address = strings.Trim(local[:i], "[]")
				port, err := strconv.Atoi(local[i+1:])
				if err == nil && port > 0 && port <= 65535 {
					socket.Port = port
				}
			}
		}
	}
	flush()
	result := []model.Process{}
	for _, p := range byPID {
		if len(p.Sockets) == 0 {
			continue
		}
		sort.Slice(p.Sockets, func(i, j int) bool {
			a, b := p.Sockets[i], p.Sockets[j]
			if a.Port != b.Port {
				return a.Port < b.Port
			}
			return fmt.Sprint(a) < fmt.Sprint(b)
		})
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PID < result[j].PID })
	return result
}
