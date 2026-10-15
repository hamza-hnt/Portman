package discovery

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"portman/internal/model"
	"portman/internal/platform"
)

// ProcFS discovers sockets in the caller's network namespace without lsof,
// root access or cgo. Root is injectable for filesystem fixtures.
type ProcFS struct {
	Inspector model.Inspector
	Root      string
}

func (p *ProcFS) Scan(ctx context.Context, all bool) (model.Snapshot, error) {
	start := time.Now()
	snapshot := model.Snapshot{At: start, Processes: []model.Process{}}
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return snapshot, err
	}
	// Capture identities before reading sockets. A process that appears during
	// the scan cannot acquire metadata from a previous owner of its PID.
	candidates := []model.Process{}
	restricted := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		process, err := p.Inspector.Inspect(ctx, pid)
		if err != nil {
			if !errors.Is(err, platform.ErrGone) {
				restricted++
			}
			continue
		}
		if !process.Zombie {
			candidates = append(candidates, process)
		}
	}
	sockets := map[string]model.Socket{}
	for _, table := range []struct{ name, protocol, family string }{
		{"tcp", "TCP", "IPv4"}, {"tcp6", "TCP", "IPv6"},
		{"udp", "UDP", "IPv4"}, {"udp6", "UDP", "IPv6"},
	} {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		file, err := os.Open(filepath.Join(p.Root, "net", table.name))
		// IPv6 tables can be absent when IPv6 is disabled in the kernel.
		if errors.Is(err, os.ErrNotExist) && table.family == "IPv6" {
			continue
		}
		if err != nil {
			return snapshot, fmt.Errorf("read /proc/net/%s: %w", table.name, err)
		}
		parsed, parseErr := parseProcNet(file, table.protocol, table.family, all)
		file.Close()
		if parseErr != nil {
			return snapshot, parseErr
		}
		for inode, socket := range parsed {
			sockets[inode] = socket
		}
	}
	for _, process := range candidates {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		fdDir := filepath.Join(p.Root, strconv.Itoa(process.PID), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				restricted++
			}
			continue
		}
		seen := map[model.Socket]bool{}
		for _, fd := range fds {
			if err := ctx.Err(); err != nil {
				return snapshot, err
			}
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if socket, ok := sockets[inode]; ok && !seen[socket] {
				process.Sockets = append(process.Sockets, socket)
				seen[socket] = true
			}
		}
		if len(process.Sockets) == 0 {
			continue
		}
		after, err := p.Inspector.Inspect(ctx, process.PID)
		if err != nil || after.Zombie || after.Identity != process.Identity || after.UID != process.UID || after.Executable != process.Executable {
			continue
		}
		sort.Slice(process.Sockets, func(i, j int) bool {
			a, b := process.Sockets[i], process.Sockets[j]
			if a.Port != b.Port {
				return a.Port < b.Port
			}
			return fmt.Sprint(a) < fmt.Sprint(b)
		})
		snapshot.Processes = append(snapshot.Processes, process)
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if restricted > 0 {
		snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("Limited /proc access: %d processes could not be inspected; their ports may be hidden", restricted))
	}
	sort.Slice(snapshot.Processes, func(i, j int) bool { return snapshot.Processes[i].PID < snapshot.Processes[j].PID })
	snapshot.Duration = time.Since(start)
	return snapshot, nil
}

func parseProcNet(r io.Reader, protocol, family string, all bool) (map[string]model.Socket, error) {
	result := map[string]model.Socket{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[0] == "sl" {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		address, port, err := procAddress(fields[1], family)
		if err != nil || port == 0 {
			continue
		}
		remoteAddress, remotePort, err := procAddress(fields[2], family)
		if err != nil {
			continue
		}
		state := "BOUND"
		if protocol == "TCP" {
			states := map[string]string{"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1", "05": "FIN_WAIT2", "06": "TIME_WAIT", "07": "CLOSED", "08": "CLOSE_WAIT", "09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING", "0C": "NEW_SYN_RECV"}
			state = states[strings.ToUpper(fields[3])]
			if state == "" || (!all && state != "LISTEN") {
				continue
			}
		}
		remote := ""
		if remotePort != 0 {
			remote = net.JoinHostPort(remoteAddress, strconv.Itoa(remotePort))
		}
		result[fields[9]] = model.Socket{Protocol: protocol, Family: family, Address: address, Port: port, Remote: remote, State: state}
	}
	return result, scanner.Err()
}

func procAddress(value, family string) (string, int, error) {
	host, portText, ok := strings.Cut(value, ":")
	length := 8
	if family == "IPv6" {
		length = 32
	}
	if !ok || len(host) != length {
		return "", 0, fmt.Errorf("invalid proc address %q", value)
	}
	port, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return "", 0, err
	}
	ip := make(net.IP, length/2)
	// /proc prints native-endian 32-bit words, including each IPv6 word.
	for i := 0; i < len(host); i += 8 {
		word, err := strconv.ParseUint(host[i:i+8], 16, 32)
		if err != nil {
			return "", 0, err
		}
		binary.NativeEndian.PutUint32(ip[i/2:i/2+4], uint32(word))
	}
	return ip.String(), int(port), nil
}
