package engine

import (
	"context"
	"testing"

	"portman/internal/model"
	"portman/internal/process"
)

func TestMergeDockerPortsKeepsUnrelatedHostSockets(t *testing.T) {
	tcp := model.Socket{Port: 8080, Protocol: "TCP", State: "LISTEN"}
	udp := model.Socket{Port: 8080, Protocol: "UDP", State: "BOUND"}
	other := model.Socket{Port: 9000, Protocol: "TCP", State: "LISTEN"}
	hosts := []model.Process{{Name: "com.docker.backend", Sockets: []model.Socket{tcp, udp, other}}, {Name: "server", Sockets: []model.Socket{tcp}}}
	container := model.Process{Name: "api", Container: &model.Container{ID: "id"}, Sockets: []model.Socket{tcp}}
	got := mergeContainers(hosts, []model.Process{container})
	if len(got) != 3 || len(got[0].Sockets) != 2 || got[0].Sockets[0].Protocol != "UDP" || len(got[1].Sockets) != 1 {
		t.Fatalf("lost unrelated sockets: %+v", got)
	}
	got = mergeContainers(hosts, nil)
	if len(got) != 2 || len(got[0].Sockets) != 3 {
		t.Fatal("Docker unavailable must preserve host discovery")
	}
}

type containerStub struct{ received []model.Process }

func (*containerStub) Discover(context.Context) ([]model.Process, error) { return nil, nil }
func (s *containerStub) Terminate(_ context.Context, p []model.Process, _ bool) []process.Result {
	s.received = p
	return []process.Result{{Process: p[0], Status: "exited"}}
}

func TestContainersNeverReachHostPIDManager(t *testing.T) {
	s := &containerStub{}
	e := &Engine{Docker: s} // nil host manager deliberately traps misrouting
	p := model.Process{Identity: model.Identity{PID: 42}, Container: &model.Container{ID: "abc", StartedAt: "start", Endpoint: "unix:///tmp/docker.sock"}}
	r := e.Terminate(context.Background(), []model.Process{p, p}, false)
	if len(r) != 1 || len(s.received) != 1 || s.received[0].Container.ID != "abc" {
		t.Fatal("container routing/dedup failed")
	}
	e.Docker = nil
	r = e.Terminate(context.Background(), []model.Process{p}, false)
	if len(r) != 1 || r[0].Status != "refused" {
		t.Fatal("must fail closed when Docker unavailable")
	}
}
