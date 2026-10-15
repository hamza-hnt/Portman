//go:build darwin && cgo

package platform

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <errno.h>
#include <string.h>

static int portman_bsd(int pid, struct proc_bsdinfo *info) {
    errno = 0;
    int n = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, info, sizeof(*info));
    if (n != sizeof(*info)) return errno ? errno : ESRCH;
    return 0;
}
static int portman_cwd(int pid, char *path) {
    struct proc_vnodepathinfo info;
    memset(&info, 0, sizeof(info));
    if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &info, sizeof(info)) != sizeof(info)) return 0;
    strlcpy(path, info.pvi_cdir.vip_path, PROC_PIDPATHINFO_MAXSIZE);
    return 1;
}
*/
import "C"

import (
	"context"
	"fmt"
	"syscall"
	"unsafe"

	"portman/internal/model"
)

func Supported() error { return nil }

func (Inspector) Inspect(ctx context.Context, pid int) (model.Process, error) {
	if err := ctx.Err(); err != nil {
		return model.Process{}, err
	}
	if pid <= 0 {
		return model.Process{}, ErrGone
	}
	var b C.struct_proc_bsdinfo
	if code := C.portman_bsd(C.int(pid), &b); code != 0 {
		if syscall.Errno(code) == syscall.ESRCH {
			return model.Process{}, ErrGone
		}
		return model.Process{}, fmt.Errorf("inspect PID %d: %w", pid, syscall.Errno(code))
	}
	p := model.Process{Identity: model.Identity{PID: pid, StartID: uint64(b.pbi_start_tvsec)*1_000_000 + uint64(b.pbi_start_tvusec)}, PPID: int(b.pbi_ppid), UID: int(b.pbi_uid), Zombie: b.pbi_status == 5}
	p.Name = C.GoString(&b.pbi_name[0])
	if p.Name == "" {
		p.Name = C.GoString(&b.pbi_comm[0])
	}
	var path [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	if C.proc_pidpath(C.int(pid), unsafe.Pointer(&path[0]), C.uint32_t(len(path))) > 0 {
		p.Executable = C.GoString(&path[0])
	}
	var cwd [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	if C.portman_cwd(C.int(pid), &cwd[0]) != 0 {
		p.CWD = C.GoString(&cwd[0])
	}
	// Metadata reads can race with exit/reuse. Discard a mixed incarnation.
	var after C.struct_proc_bsdinfo
	if C.portman_bsd(C.int(pid), &after) != 0 || b.pbi_start_tvsec != after.pbi_start_tvsec || b.pbi_start_tvusec != after.pbi_start_tvusec {
		return model.Process{}, ErrGone
	}
	return p, nil
}
