package process

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"portman/internal/model"
	"portman/internal/platform"
)

type inspectFunc func(context.Context, int) (model.Process, error)

func (f inspectFunc) Inspect(ctx context.Context, pid int) (model.Process, error) { return f(ctx, pid) }

func target() model.Process {
	return model.Process{Identity: model.Identity{PID: 42, StartID: 123}, UID: 501, Executable: "/tmp/server", Name: "server"}
}
func testManager(f inspectFunc) *Manager {
	return &Manager{Inspector: f, UID: 501, ProtectedPIDs: map[int]bool{99: true}, Grace: time.Millisecond, Poll: time.Millisecond}
}

func TestRefusesUnsafeTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model.Process)
	}{
		{"init", func(p *model.Process) { p.PID = 1 }},
		{"root", func(p *model.Process) { p.UID = 0 }},
		{"other user", func(p *model.Process) { p.UID = 502 }},
		{"ancestor", func(p *model.Process) { p.PID = 99 }},
		{"system", func(p *model.Process) { p.Executable = "/System/Library/tool" }},
		{"linux system", func(p *model.Process) { p.Executable = "/usr/lib/systemd/systemd" }},
		{"linux critical name", func(p *model.Process) { p.Name = "dbus-daemon" }},
		{"identity missing", func(p *model.Process) { p.StartID = 0 }},
		{"path missing", func(p *model.Process) { p.Executable = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := target()
			tc.mutate(&p)
			m := testManager(nil)
			m.Signal = func(int, syscall.Signal) error { t.Fatal("unsafe signal"); return nil }
			r := m.Terminate(context.Background(), []model.Process{p}, false)
			if r[0].Status != "refused" {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestRevalidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current model.Process
		err     error
		status  string
	}{
		{"reused", model.Process{Identity: model.Identity{PID: 42, StartID: 124}}, nil, "exited"},
		{"gone", model.Process{}, platform.ErrGone, "exited"},
		{"denied", model.Process{}, syscall.EPERM, "refused"},
		{"exec changed", func() model.Process { p := target(); p.Executable = "/tmp/other"; return p }(), nil, "refused"},
		{"exec unavailable", func() model.Process { p := target(); p.Executable = ""; return p }(), nil, "refused"},
		{"uid changed", func() model.Process { p := target(); p.UID = 0; return p }(), nil, "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(func(context.Context, int) (model.Process, error) { return tc.current, tc.err })
			m.Signal = func(int, syscall.Signal) error { t.Fatal("must not signal changed identity"); return nil }
			r := m.Terminate(context.Background(), []model.Process{target()}, false)
			if r[0].Status != tc.status {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestExecutableDisappearsBeforeExitState(t *testing.T) {
	for _, exits := range []bool{true, false} {
		sent, polls := false, 0
		m := testManager(func(context.Context, int) (model.Process, error) {
			p := target()
			if sent {
				polls++
				if exits && polls > 1 {
					return p, platform.ErrGone
				}
				p.Executable = ""
			}
			return p, nil
		})
		m.Grace = 20 * time.Millisecond
		m.Signal = func(int, syscall.Signal) error {
			if sent {
				t.Fatal("must never resend or escalate while polling")
			}
			sent = true
			return nil
		}
		results := m.Terminate(context.Background(), []model.Process{target()}, false)
		want := "running"
		if exits {
			want = "exited"
		}
		if results[0].Status != want {
			t.Fatalf("exits %v: %+v", exits, results)
		}
	}
}

func TestTermNeverAutomaticallyEscalates(t *testing.T) {
	p := target()
	signals := []syscall.Signal{}
	m := testManager(func(context.Context, int) (model.Process, error) { return p, nil })
	m.Signal = func(pid int, s syscall.Signal) error { signals = append(signals, s); return nil }
	r := m.Terminate(context.Background(), []model.Process{p, p}, false)
	if len(r) != 1 || r[0].Status != "running" || len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Fatalf("results %+v signals %+v", r, signals)
	}
	m.Terminate(context.Background(), []model.Process{p}, true)
	if len(signals) != 2 || signals[1] != syscall.SIGKILL {
		t.Fatal("explicit escalation missing")
	}
}

func TestExitAndSignalErrors(t *testing.T) {
	for _, signalErr := range []error{nil, syscall.EPERM, syscall.ESRCH} {
		alive := true
		m := testManager(func(context.Context, int) (model.Process, error) {
			if !alive {
				return model.Process{}, platform.ErrGone
			}
			return target(), nil
		})
		m.Signal = func(int, syscall.Signal) error { alive = false; return signalErr }
		r := m.Terminate(context.Background(), []model.Process{target()}, false)
		want := "exited"
		if errors.Is(signalErr, syscall.EPERM) {
			want = "error"
		}
		if r[0].Status != want {
			t.Fatalf("%v: %+v", signalErr, r)
		}
	}
}
