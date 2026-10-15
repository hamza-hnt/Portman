package platform

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestInspectLinuxSelfAndRejectStaleSignal(t *testing.T) {
	ctx := context.Background()
	p, err := (Inspector{}).Inspect(ctx, os.Getpid())
	if err != nil || p.PPID != os.Getppid() || p.UID != os.Geteuid() || p.Executable == "" || p.CWD == "" || p.StartID == 0 {
		t.Fatalf("%+v / %v", p, err)
	}
	// Signal 0 only probes access. Never send a real signal to the test runner.
	p.StartID++
	if err := Signal(ctx, p, syscall.Signal(0)); !errors.Is(err, ErrGone) {
		t.Fatalf("stale identity: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Inspector{}).Inspect(ctx, os.Getpid()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
