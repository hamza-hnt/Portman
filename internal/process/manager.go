// Package process implements guarded, identity-pinned process termination.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"portman/internal/classify"
	"portman/internal/model"
	"portman/internal/platform"
)

type Result struct {
	Process model.Process
	Status  string // exited, running, refused, error
	Detail  string
}

var errExecutableUnavailable = errors.New("process executable unavailable; refresh and select again")

type Manager struct {
	Inspector     model.Inspector
	Signal        func(int, syscall.Signal) error
	UID           int
	ProtectedPIDs map[int]bool
	Grace         time.Duration
	Poll          time.Duration
}

func New(inspector model.Inspector) *Manager {
	m := &Manager{Inspector: inspector, UID: os.Getuid(), ProtectedPIDs: map[int]bool{}, Grace: 2 * time.Second, Poll: 80 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for pid, depth := os.Getpid(), 0; pid > 1 && depth < 64; depth++ {
		if m.ProtectedPIDs[pid] {
			break
		}
		m.ProtectedPIDs[pid] = true
		p, err := inspector.Inspect(ctx, pid)
		if err != nil {
			break
		}
		pid = p.PPID
	}
	return m
}

func (m *Manager) Protection(p model.Process) string {
	if p.Container != nil {
		return "containers must be managed through Docker, never a host PID"
	}
	if classify.IsDockerHost(p) {
		return "Docker engine; stop an individual container instead"
	}
	if p.Protected != "" {
		return p.Protected
	}
	if p.PID <= 1 {
		return "kernel / init process"
	}
	if p.StartID == 0 || p.Executable == "" {
		return "process identity unavailable"
	}
	if p.UID == 0 {
		return "root-owned process"
	}
	if p.UID != m.UID {
		return "owned by another user"
	}
	if m.ProtectedPIDs[p.PID] {
		return "Portman or its terminal ancestry"
	}
	if p.Category == model.System || strings.HasPrefix(p.Executable, "/System/") || strings.HasPrefix(p.Executable, "/usr/libexec/") || strings.HasPrefix(p.Executable, "/usr/sbin/") || strings.HasPrefix(p.Executable, "/usr/lib/systemd/") || strings.HasPrefix(p.Executable, "/lib/systemd/") || strings.HasPrefix(p.Executable, "/sbin/") {
		return "system process"
	}
	switch p.Name {
	case "launchd", "kernel_task", "WindowServer", "loginwindow", "securityd", "opendirectoryd", "sudo", "systemd", "init", "dbus-daemon", "dbus-broker", "sshd", "login":
		return "critical system process"
	}
	return ""
}

func (m *Manager) verify(ctx context.Context, expected model.Process) (model.Process, error) {
	current, err := m.Inspector.Inspect(ctx, expected.PID)
	if err != nil {
		return current, err
	}
	if current.Identity != expected.Identity || current.Zombie {
		return current, platform.ErrGone
	}
	if current.UID != expected.UID || (current.Executable != "" && current.Executable != expected.Executable) {
		return current, errors.New("process ownership or executable changed; refresh and select again")
	}
	if current.Executable == "" {
		return current, errExecutableUnavailable
	}
	return current, nil
}

// Terminate never escalates automatically. A force call must come from a second
// explicit UI confirmation using only survivors of the preceding TERM request.
func (m *Manager) Terminate(ctx context.Context, targets []model.Process, force bool) []Result {
	results := []Result{}
	seen := map[int]bool{}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	for _, p := range targets {
		if seen[p.PID] {
			continue
		}
		seen[p.PID] = true
		r := Result{Process: p}
		if reason := m.Protection(p); reason != "" {
			r.Status = "refused"
			r.Detail = reason
			results = append(results, r)
			continue
		}
		current, err := m.verify(ctx, p)
		if errors.Is(err, platform.ErrGone) {
			r.Status = "exited"
			r.Detail = "already exited"
		} else if err != nil {
			r.Status = "refused"
			r.Detail = err.Error()
		} else if reason := m.Protection(current); reason != "" {
			r.Status = "refused"
			r.Detail = reason
		} else if err = m.send(ctx, current, signal); errors.Is(err, syscall.ESRCH) || errors.Is(err, platform.ErrGone) {
			r.Status = "exited"
		} else if err != nil {
			r.Status = "error"
			r.Detail = err.Error()
		} else {
			r.Status = "running"
			r.Detail = fmt.Sprintf("%s sent", signal)
		}
		results = append(results, r)
	}
	grace := m.Grace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	poll := m.Poll
	if poll <= 0 {
		poll = 80 * time.Millisecond
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		pending := false
		for i := range results {
			r := &results[i]
			if r.Status != "running" {
				continue
			}
			_, err := m.verify(ctx, r.Process)
			if errors.Is(err, platform.ErrGone) {
				r.Status = "exited"
				r.Detail = "process exited"
			} else if errors.Is(err, errExecutableUnavailable) {
				// Linux can detach the executable before publishing zombie state.
				// Wait for exit without assuming it or sending another signal.
				pending = true
			} else if err != nil {
				r.Status = "error"
				r.Detail = err.Error()
			} else {
				pending = true
			}
		}
		if !pending {
			return results
		}
		select {
		case <-ctx.Done():
			return results
		case <-deadline.C:
			return results
		case <-ticker.C:
		}
	}
}

func (m *Manager) send(ctx context.Context, p model.Process, signal syscall.Signal) error {
	if m.Signal != nil {
		return m.Signal(p.PID, signal)
	}
	return platform.Signal(ctx, p, signal)
}
