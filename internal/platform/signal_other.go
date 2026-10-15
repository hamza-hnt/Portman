//go:build !linux

package platform

import (
	"context"
	"syscall"

	"portman/internal/model"
)

func Signal(ctx context.Context, p model.Process, signal syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return syscall.Kill(p.PID, signal)
}
