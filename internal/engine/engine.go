// Package engine composes discovery, resolution, classification, and policy.
package engine

import (
	"context"
	"portman/internal/classify"
	"portman/internal/model"
	"portman/internal/process"
	"portman/internal/project"
	"strings"
	"time"
)

type Engine struct {
	Discovery model.Discoverer
	Inspector model.Inspector
	Manager   *process.Manager
	Docker    ContainerEngine
}

type ContainerEngine interface {
	Discover(context.Context) ([]model.Process, error)
	Terminate(context.Context, []model.Process, bool) []process.Result
}

func (e *Engine) Scan(ctx context.Context, all bool) (model.Snapshot, error) {
	start := time.Now()
	s, err := e.Discovery.Scan(ctx, all)
	if err != nil {
		return s, err
	}
	r := project.New(e.Inspector)
	for i := range s.Processes {
		p := &s.Processes[i]
		p.Project = r.Resolve(ctx, *p)
		p.Category = classify.Category(*p)
		p.Protected = e.Manager.Protection(*p)
	}
	if e.Docker != nil {
		containers, err := e.Docker.Discover(ctx)
		if err != nil {
			s.Warnings = append(s.Warnings, "Docker: "+err.Error())
		} else {
			s.Processes = mergeContainers(s.Processes, containers)
		}
	}
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	s.Duration = time.Since(start)
	return s, nil
}

// Remove only Docker helper sockets attributed to a published container port.
// Other host sockets stay visible and protected, including on partial failures.
func mergeContainers(hosts, containers []model.Process) []model.Process {
	type binding struct {
		port     int
		protocol string
	}
	ports := map[binding]bool{}
	for _, p := range containers {
		for _, s := range p.Sockets {
			ports[binding{s.Port, strings.ToUpper(s.Protocol)}] = true
		}
	}
	result := make([]model.Process, 0, len(hosts)+len(containers))
	for _, p := range hosts {
		if classify.IsDockerHost(p) {
			sockets := []model.Socket{}
			for _, s := range p.Sockets {
				if !ports[binding{s.Port, strings.ToUpper(s.Protocol)}] || (s.State != "LISTEN" && s.State != "BOUND") {
					sockets = append(sockets, s)
				}
			}
			p.Sockets = sockets
			if len(sockets) == 0 {
				continue
			}
		}
		result = append(result, p)
	}
	return append(result, containers...)
}

// Partition before invoking either backend: VM PIDs never reach host signaling.
func (e *Engine) Terminate(ctx context.Context, targets []model.Process, force bool) []process.Result {
	var hosts, containers []model.Process
	seen := map[string]bool{}
	for _, p := range targets {
		if seen[p.Key()] {
			continue
		}
		seen[p.Key()] = true
		if p.Container != nil {
			containers = append(containers, p)
		} else {
			hosts = append(hosts, p)
		}
	}
	results := []process.Result{}
	if len(hosts) > 0 {
		results = e.Manager.Terminate(ctx, hosts, force)
	}
	if len(containers) > 0 {
		if e.Docker != nil {
			results = append(results, e.Docker.Terminate(ctx, containers, force)...)
		} else {
			for _, p := range containers {
				results = append(results, process.Result{Process: p, Status: "refused", Detail: "Docker unavailable"})
			}
		}
	}
	return results
}
