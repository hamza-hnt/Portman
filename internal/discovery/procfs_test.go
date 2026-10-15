package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"portman/internal/model"
	"portman/internal/platform"
)

const procTCP = "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n" +
	"0: 0100007F:0BB8 00000000:0000 0A 0:0 00:0 0 1000 0 123 1\n" +
	"1: 0100007F:C350 01010101:01BB 01 0:0 00:0 0 1000 0 124 1\n" +
	"2: 0100007F:0000 00000000:0000 0A 0:0 00:0 0 1000 0 125 1\n" +
	"3: 0100007F:1234 00000000:0000 0A 0:0 00:0 0 1000 0 0 1\n"

func TestProcNetSockets(t *testing.T) {
	for _, all := range []bool{false, true} {
		sockets, err := parseProcNet(strings.NewReader(procTCP), "TCP", "IPv4", all)
		want := 1
		if all {
			want = 2
		}
		if err != nil || len(sockets) != want || sockets["123"].Address != "127.0.0.1" || sockets["123"].Port != 3000 || sockets["123"].State != "LISTEN" {
			t.Fatalf("%+v / %v", sockets, err)
		}
		if all && sockets["124"].Remote != "1.1.1.1:443" {
			t.Fatal(sockets)
		}
	}
	sockets, err := parseProcNet(strings.NewReader("0: 00000000000000000000000001000000:14E9 00000000000000000000000000000000:0000 07 0:0 00:0 0 1000 0 456\n"), "UDP", "IPv6", false)
	if err != nil || sockets["456"].Address != "::1" || sockets["456"].Port != 5353 || sockets["456"].State != "BOUND" {
		t.Fatalf("%+v / %v", sockets, err)
	}
	for _, value := range []string{"", "0100007F:10000", "ZZ00007F:1234", "0100007F"} {
		if _, _, err := procAddress(value, "IPv4"); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestProcScanRevalidatesIdentityAndDeduplicatesFDs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"net", "42/fd"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"tcp": procTCP, "udp": ""} {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, fd := range []string{"3", "4"} {
		if err := os.Symlink("socket:[123]", filepath.Join(root, "42/fd", fd)); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"same", "reused", "gone", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			scanner := ProcFS{Root: root, Inspector: inspectFunc(func(context.Context, int) (model.Process, error) {
				calls++
				p := model.Process{Identity: model.Identity{PID: 42, StartID: 100}, UID: 1000, Executable: "/usr/bin/node"}
				if mode == "restricted" {
					return p, os.ErrPermission
				}
				if calls > 1 && mode == "reused" {
					p.StartID++
				}
				if calls > 1 && mode == "gone" {
					return p, platform.ErrGone
				}
				return p, nil
			})}
			s, err := scanner.Scan(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "same" {
				if len(s.Processes) != 1 || len(s.Processes[0].Sockets) != 1 {
					t.Fatal(s)
				}
			} else if len(s.Processes) != 0 {
				t.Fatal("mixed identity accepted", s)
			}
			if mode == "restricted" && len(s.Warnings) == 0 {
				t.Fatal("missing visibility warning")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := scanner.Scan(ctx, false); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
	if err := os.Remove(filepath.Join(root, "net/tcp")); err != nil {
		t.Fatal(err)
	}
	scanner := ProcFS{Root: root, Inspector: inspectFunc(func(context.Context, int) (model.Process, error) { return model.Process{}, platform.ErrGone })}
	if _, err := scanner.Scan(context.Background(), false); err == nil {
		t.Fatal("required socket table failure silently ignored")
	}
}
