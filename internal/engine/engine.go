// Package engine composes discovery, resolution, classification, and policy.
package engine

import (
	"context"
	"portman/internal/classify"
	"portman/internal/model"
	"portman/internal/process"
	"portman/internal/project"
	"time"
)

type Engine struct {
	Discovery model.Discoverer
	Inspector model.Inspector
	Manager   *process.Manager
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
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	s.Duration = time.Since(start)
	return s, nil
}
