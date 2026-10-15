package docker

import (
	"context"
	"errors"
	"net/http"
	"time"

	"portman/internal/model"
	"portman/internal/process"
)

// Terminate targets immutable IDs on the captured local daemon. SIGTERM never
// escalates inside Docker (unlike docker stop's default timeout).
func (c *Client) Terminate(ctx context.Context, targets []model.Process, force bool) []process.Result {
	results := make([]process.Result, len(targets))
	// Bound concurrency so a Compose group shares one grace period, rather than
	// waiting serially for each stubborn container.
	sem := make(chan struct{}, 4)
	done := make(chan int, len(targets))
	for i, p := range targets {
		go func(i int, p model.Process) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = process.Result{Process: p, Status: "error", Detail: ctx.Err().Error()}
				done <- i
				return
			}
			results[i] = c.terminate(ctx, p, force)
			<-sem
			done <- i
		}(i, p)
	}
	for range targets {
		<-done
	}
	return results
}

func (c *Client) terminate(ctx context.Context, p model.Process, force bool) process.Result {
	r := process.Result{Process: p, Status: "refused"}
	target := p.Container
	if target == nil || !fullID.MatchString(target.ID) || target.StartedAt == "" {
		r.Detail = "container identity unavailable"
		return r
	}
	if p.Protected != "" {
		r.Detail = p.Protected
		return r
	}
	a, err := connect(target.Endpoint)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	defer a.transport.CloseIdleConnections()
	verify := func() (bool, error) {
		info, err := a.inspect(ctx, target.ID)
		if errors.Is(err, errMissing) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.State.StartedAt != target.StartedAt {
			return false, errors.New("container restarted; refresh and select again")
		}
		if !info.State.Running {
			return false, nil
		}
		if reason := workload(target.Endpoint, info).Protected; reason != "" {
			return false, errors.New(reason)
		}
		return true, nil
	}
	running, err := verify()
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	if !running {
		r.Status = "exited"
		r.Detail = "already stopped"
		return r
	}
	signal := "SIGTERM"
	if force {
		signal = "SIGKILL"
	}
	if err := a.request(ctx, http.MethodPost, "/containers/"+target.ID+"/kill?signal="+signal, nil); err != nil {
		if errors.Is(err, errMissing) {
			r.Status = "exited"
			r.Detail = "already removed"
		} else {
			r.Status = "error"
			r.Detail = err.Error()
		}
		return r
	}
	r.Status = "running"
	r.Detail = signal + " sent to container"
	grace := c.Grace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	poll := c.Poll
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		running, err := verify()
		if err != nil {
			r.Status = "refused"
			r.Detail = err.Error()
			return r
		}
		if !running {
			r.Status = "exited"
			r.Detail = "container stopped"
			return r
		}
		select {
		case <-ctx.Done():
			r.Status = "error"
			r.Detail = "container state unknown: request timed out"
			return r
		case <-deadline.C:
			return r
		case <-ticker.C:
		}
	}
}
