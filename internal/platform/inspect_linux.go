package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"portman/internal/model"
)

func Supported() error {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return fmt.Errorf("Linux requires a mounted /proc filesystem: %w", err)
	}
	return nil
}

func procError(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return ErrGone
	}
	return err
}

func (Inspector) Inspect(ctx context.Context, pid int) (model.Process, error) {
	if err := ctx.Err(); err != nil {
		return model.Process{}, err
	}
	if pid <= 0 {
		return model.Process{}, ErrGone
	}
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	readStat := func() (model.Process, error) {
		data, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			return model.Process{}, procError(err)
		}
		return parseProcStat(data, pid)
	}
	p, err := readStat()
	if err != nil {
		return p, err
	}
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return p, procError(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) == 5 {
				p.UID, err = strconv.Atoi(fields[2]) // effective UID, as on macOS
				if err != nil {
					return p, err
				}
				for _, uid := range fields[1:] {
					if uid != fields[2] {
						p.Protected = "process has elevated or mixed user credentials"
					}
				}
			}
		}
	}
	if p.UID < 0 {
		return p, fmt.Errorf("UID unavailable for PID %d", pid)
	}
	// Restricted paths remain empty and cannot be signaled by the manager.
	p.Executable, _ = os.Readlink(filepath.Join(dir, "exe"))
	p.CWD, _ = os.Readlink(filepath.Join(dir, "cwd"))
	after, err := readStat()
	if err != nil {
		return model.Process{}, err
	}
	if after.Identity != p.Identity {
		return model.Process{}, ErrGone
	}
	// Exit can clear exe/cwd between the first stat and the symlink reads.
	// Use the final state so polling reports an exit, not an executable change.
	p.Zombie = after.Zombie
	if err := ctx.Err(); err != nil {
		return model.Process{}, err
	}
	return p, nil
}
