package discovery

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"portman/internal/model"
	"portman/internal/platform"
)

func TestParseSockets(t *testing.T) {
	data := []byte("p42\x00cnode\x00\nf1\x00tIPv4\x00PTCP\x00n127.0.0.1:3000\x00TST=LISTEN\x00\nf2\x00tIPv6\x00PTCP\x00n[::1]:3000\x00TST=LISTEN\x00\nf3\x00tIPv4\x00PUDP\x00n*:5353\x00\nf4\x00tIPv4\x00PUDP\x00n*:*\x00\nf5\x00tIPv4\x00PTCP\x00n127.0.0.1:51000->1.1.1.1:443\x00TST=ESTABLISHED\x00\nf6\x00tIPv4\x00PTCP\x00n127.0.0.1:3000\x00TST=LISTEN\x00\np7\x00cbroken\x00\nf1\x00PTCP\x00n*:99999\x00TST=LISTEN\x00\n")
	p := Parse(data, false)
	if len(p) != 1 || p[0].PID != 42 || len(p[0].Sockets) != 3 {
		t.Fatalf("unexpected listeners: %+v", p)
	}
	if p[0].Sockets[1].Address != "::1" || p[0].Sockets[2].State != "BOUND" {
		t.Fatalf("bad metadata: %+v", p[0].Sockets)
	}
	p = Parse(data, true)
	if len(p[0].Sockets) != 4 || p[0].Sockets[3].Remote != "1.1.1.1:443" {
		t.Fatalf("bad all-connections parse: %+v", p)
	}
	if !reflect.DeepEqual(Parse(nil, false), []model.Process{}) {
		t.Fatal("empty input should yield empty slice")
	}
}

type inspectFunc func(context.Context, int) (model.Process, error)

func (f inspectFunc) Inspect(ctx context.Context, pid int) (model.Process, error) { return f(ctx, pid) }

func TestScanRestrictedAndDisappearing(t *testing.T) {
	raw := []byte("p42\x00cnode\x00\nf1\x00PTCP\x00n*:3000\x00TST=LISTEN\x00\n")
	for _, tc := range []struct {
		name      string
		p         model.Process
		err       error
		count     int
		protected bool
	}{
		{"gone", model.Process{}, platform.ErrGone, 0, false},
		{"restricted", model.Process{}, errors.New("permission denied"), 1, true},
		{"reused during scan", model.Process{Identity: model.Identity{PID: 42, StartID: ^uint64(0)}}, nil, 0, false},
		{"visible", model.Process{Identity: model.Identity{PID: 42, StartID: 1}, Name: "node"}, nil, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &Lsof{Inspector: inspectFunc(func(context.Context, int) (model.Process, error) { return tc.p, tc.err }), Run: func(context.Context) ([]byte, []byte, error) { return raw, nil, nil }}
			s, err := l.Scan(context.Background(), false)
			if err != nil || len(s.Processes) != tc.count {
				t.Fatalf("%+v / %v", s, err)
			}
			if tc.count > 0 && (s.Processes[0].Protected != "") != tc.protected {
				t.Fatal("incorrect protection")
			}
		})
	}
}

func TestScanErrorsAndEmptyExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 1")
	exitOne := cmd.Run()
	for _, tc := range []struct {
		err       error
		stderr    []byte
		wantError bool
	}{{exitOne, nil, false}, {exitOne, []byte("permission error"), true}, {errors.New("not installed"), nil, true}} {
		l := &Lsof{Run: func(context.Context) ([]byte, []byte, error) { return nil, tc.stderr, tc.err }}
		_, err := l.Scan(context.Background(), false)
		if (err != nil) != tc.wantError {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("p42\x00cnode\x00\nf1\x00PTCP\x00n*:3000\x00TST=LISTEN\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, p := range Parse(b, true) {
			if p.PID <= 0 {
				t.Fatal("invalid PID")
			}
			for _, s := range p.Sockets {
				if s.Port < 1 || s.Port > 65535 {
					t.Fatal("invalid port")
				}
			}
		}
	})
}
