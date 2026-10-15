//go:build (darwin && cgo) || linux

package integration

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"portman/internal/discovery"
	"portman/internal/engine"
	"portman/internal/model"
	"portman/internal/platform"
	"portman/internal/process"
)

// This subprocess fixture owns both sockets. Tests only ever signal this child.
func TestListenerHelper(t *testing.T) {
	if os.Getenv("PORTMAN_TEST_HELPER") == "" {
		return
	}
	if os.Getenv("PORTMAN_TEST_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	ch := make(chan os.Signal, 1)
	if os.Getenv("PORTMAN_TEST_IGNORE_TERM") != "1" {
		signal.Notify(ch, syscall.SIGTERM)
	}
	json.NewEncoder(os.Stdout).Encode([]int{tcp.Addr().(*net.TCPAddr).Port, udp.LocalAddr().(*net.UDPAddr).Port})
	if os.Getenv("PORTMAN_TEST_IGNORE_TERM") == "1" {
		select {}
	}
	<-ch
	tcp.Close()
	udp.Close()
	os.Exit(0)
}

func TestDiscoverResolveAndTerminateRealServers(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root-owned test children are intentionally protected")
	}
	for _, ignore := range []bool{false, true} {
		name := "graceful"
		if ignore {
			name = "explicit force"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			root, _ = filepath.EvalSymlinks(root)
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"portman-integration"}`), 0644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestListenerHelper$")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PORTMAN_TEST_HELPER=1")
			if ignore {
				cmd.Env = append(cmd.Env, "PORTMAN_TEST_IGNORE_TERM=1")
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var ports []int
			ready := make(chan error, 1)
			go func() { ready <- json.NewDecoder(stdout).Decode(&ports) }()
			select {
			case err := <-ready:
				if err != nil {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal("helper did not start")
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				cmd.Process.Kill()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("helper did not exit")
				}
			})
			inspector := platform.Inspector{}
			manager := process.New(inspector)
			// Race-instrumented Go children delay os.Exit by one second.
			if ignore {
				manager.Grace = 150 * time.Millisecond
			}
			e := engine.Engine{Discovery: discovery.New(inspector), Inspector: inspector, Manager: manager}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, err := e.Scan(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			var target *model.Process
			for _, p := range s.Processes {
				if p.PID == cmd.Process.Pid {
					p := p
					target = &p
					break
				}
			}
			if target == nil {
				t.Fatalf("child PID %d absent from scan", cmd.Process.Pid)
			}
			if target.Project == nil || target.Project.Root != root || target.Category != model.Development || target.Protected != "" {
				t.Fatalf("bad child metadata: %+v", target)
			}
			found := map[int]bool{}
			for _, socket := range target.Sockets {
				found[socket.Port] = true
			}
			if !found[ports[0]] || !found[ports[1]] {
				t.Fatalf("missing TCP/UDP ports: %+v", target.Sockets)
			}
			results := manager.Terminate(ctx, []model.Process{*target}, false)
			if ignore {
				if results[0].Status != "running" {
					t.Fatalf("TERM-ignoring helper: %+v", results)
				}
				results = manager.Terminate(ctx, []model.Process{*target}, true)
			}
			if results[0].Status != "exited" {
				t.Fatalf("server did not exit: %+v", results)
			}
		})
	}
}
