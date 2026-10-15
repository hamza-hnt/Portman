package platform

import (
	"fmt"
	"strconv"
	"strings"

	"portman/internal/model"
)

// comm may contain spaces, newlines and closing parentheses. The last ')' is
// its delimiter; splitting the entire stat line into fields is unsafe.
func parseProcStat(data []byte, pid int) (model.Process, error) {
	s := string(data)
	left, right := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if left < 1 || right <= left {
		return model.Process{}, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	actual, err := strconv.Atoi(strings.TrimSpace(s[:left]))
	fields := strings.Fields(s[right+1:])
	if err != nil || actual != pid || len(fields) < 20 {
		return model.Process{}, fmt.Errorf("incomplete /proc/%d/stat", pid)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return model.Process{}, fmt.Errorf("invalid parent PID for %d", pid)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return model.Process{}, fmt.Errorf("invalid start time for %d", pid)
	}
	return model.Process{
		Identity: model.Identity{PID: pid, StartID: start}, PPID: parent,
		UID: -1, Name: s[left+1 : right], Zombie: fields[0] == "Z" || fields[0] == "X",
	}, nil
}
