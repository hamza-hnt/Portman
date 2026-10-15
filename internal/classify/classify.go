// Package classify applies deterministic, explainable classification heuristics.
package classify

import (
	"path/filepath"
	"portman/internal/model"
	"strings"
)

func Category(p model.Process) model.Category {
	path := p.Executable
	if IsDockerHost(p) {
		return model.Docker
	}
	if p.UID == 0 || p.PID <= 1 || strings.HasPrefix(path, "/System/") || strings.HasPrefix(path, "/usr/libexec/") || strings.HasPrefix(path, "/usr/sbin/") || strings.HasPrefix(path, "/usr/lib/systemd/") || strings.HasPrefix(path, "/lib/systemd/") || strings.HasPrefix(path, "/sbin/") {
		return model.System
	}
	if IsDesktopApp(path) {
		return model.Application
	}
	if p.Project != nil {
		return model.Development
	}
	if strings.HasPrefix(path, "/opt/homebrew/") || strings.HasPrefix(path, "/usr/local/Cellar/") || strings.HasPrefix(path, "/usr/local/opt/") || strings.HasPrefix(path, "/home/linuxbrew/.linuxbrew/") {
		// Homebrew installs interactive tools too; only known daemons or
		// launchd children are treated as services.
		switch filepath.Base(path) {
		case "postgres", "postmaster", "redis-server", "mysqld", "mariadbd", "nginx", "httpd", "memcached", "mongod", "rabbitmq-server", "dnsmasq":
			return model.Homebrew
		}
		if p.PPID == 1 {
			return model.Homebrew
		}
	}
	return model.Application
}

func IsDockerHost(p model.Process) bool {
	name := strings.ToLower(p.Name)
	return p.Container == nil && (strings.Contains(strings.ToLower(p.Executable), "docker.app/") || strings.HasPrefix(name, "com.docker.") || name == "docker-proxy" || name == "dockerd" || name == "containerd")
}

// Framework Python distributions wrap the interpreter in Python.app. It is a
// development runtime, not a GUI app whose inherited CWD should be ignored.
func IsDesktopApp(path string) bool {
	if strings.Contains(path, "/Python.framework/") && strings.HasSuffix(path, "/Resources/Python.app/Contents/MacOS/Python") {
		return false
	}
	return strings.Contains(path, ".app/")
}
