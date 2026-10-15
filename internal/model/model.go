// Package model contains the shared, platform-independent domain types.
package model

import (
	"context"
	"fmt"
	"time"
)

type Category string

const (
	Development Category = "Development Projects"
	Homebrew    Category = "Homebrew Services"
	Docker      Category = "Docker"
	Application Category = "Applications"
	System      Category = "System"
)

var Categories = []Category{Development, Homebrew, Docker, Application, System}

// Identity is pinned to a process incarnation, not just a recyclable PID.
type Identity struct {
	PID     int    `json:"pid"`
	StartID uint64 `json:"start_id"` // OS-specific start token: macOS epoch microseconds; Linux ticks since boot
}

func (i Identity) Key() string { return fmt.Sprintf("%d:%d", i.PID, i.StartID) }

type Socket struct {
	Protocol string `json:"protocol"`
	Family   string `json:"family"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Remote   string `json:"remote,omitempty"`
	State    string `json:"state"`
}

type Project struct {
	Name   string `json:"name"`
	Root   string `json:"root"`
	Marker string `json:"marker"`
	Source string `json:"source"`
}

// Container identifies a Docker workload independently of host PIDs. StartedAt
// invalidates selections after a restart; Endpoint pins the local daemon.
type Container struct {
	ID             string        `json:"id"`
	StartedAt      string        `json:"started_at"`
	Endpoint       string        `json:"endpoint"`
	Image          string        `json:"image"`
	ComposeProject string        `json:"compose_project,omitempty"`
	Service        string        `json:"service,omitempty"`
	Ports          []PortMapping `json:"ports"`
}

type PortMapping struct {
	Address       string `json:"address"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// Process is a discovered workload: a host process or a container. Container
// entries have no host PID and must never be sent to the host signal manager.
type Process struct {
	Identity
	PPID       int        `json:"ppid"`
	UID        int        `json:"uid"`
	Name       string     `json:"name"`
	Executable string     `json:"executable"`
	CWD        string     `json:"cwd"`
	Zombie     bool       `json:"zombie,omitempty"`
	Sockets    []Socket   `json:"sockets"`
	Category   Category   `json:"category"`
	Project    *Project   `json:"project,omitempty"`
	Protected  string     `json:"protected,omitempty"`
	Container  *Container `json:"container,omitempty"`
}

// Key identifies one incarnation, including containers without host PIDs.
func (p Process) Key() string {
	if c := p.Container; c != nil {
		return "docker:" + c.Endpoint + ":" + c.ID + ":" + c.StartedAt
	}
	return p.Identity.Key()
}

func (p Process) DisplayID() string {
	if p.Container != nil {
		return p.Container.ID[:min(6, len(p.Container.ID))]
	}
	return fmt.Sprint(p.PID)
}

type Snapshot struct {
	Processes []Process     `json:"processes"`
	At        time.Time     `json:"at"`
	Duration  time.Duration `json:"duration_ns"`
	Warnings  []string      `json:"warnings,omitempty"`
}

type Inspector interface {
	Inspect(context.Context, int) (Process, error)
}

type Discoverer interface {
	Scan(context.Context, bool) (Snapshot, error)
}
