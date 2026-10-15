package platform

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
	"portman/internal/model"
)

// Open the handle BEFORE revalidation. If the PID is recycled afterwards,
// pidfd_send_signal still refers to the old process, never the replacement.
func Signal(ctx context.Context, expected model.Process, signal syscall.Signal) error {
	fd, err := unix.PidfdOpen(expected.PID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return ErrGone
	}
	if err != nil {
		return fmt.Errorf("safe signaling requires pidfd access (Linux 5.3+): %w", err)
	}
	defer unix.Close(fd)
	current, err := (Inspector{}).Inspect(ctx, expected.PID)
	if err != nil {
		return err
	}
	if current.Identity != expected.Identity || current.Zombie {
		return ErrGone
	}
	if current.UID != expected.UID || current.Executable != expected.Executable || current.Protected != "" {
		return errors.New("process credentials or executable changed; refresh and select again")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0)
}
