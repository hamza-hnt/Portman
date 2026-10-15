package docker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"portman/internal/model"
)

type fakeEngine struct {
	mu       sync.Mutex
	items    map[string]inspection
	signals  []string
	stubborn bool
	denied   bool
}

func fixtureInfo(id, name, project string) inspection {
	i := inspection{ID: strings.Repeat(id, 64), Name: "/" + name}
	i.State.Running = true
	i.State.StartedAt = "2026-10-15T10:00:00.123456789Z"
	i.Config.Image = "example/api:latest"
	i.Config.Labels = map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": "api", "com.docker.compose.project.working_dir": "/work/" + project}
	return i
}

func serveEngine(t *testing.T, f *fakeEngine) *Client {
	t.Helper()
	// macOS Unix socket paths are capped at 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "pm-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.denied {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/containers/json" {
			var entries []map[string]string
			for id, i := range f.items {
				if i.State.Running {
					entries = append(entries, map[string]string{"Id": id})
				}
			}
			json.NewEncoder(w).Encode(entries)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 4 {
			w.WriteHeader(404)
			return
		}
		i, ok := f.items[parts[2]]
		if !ok {
			w.WriteHeader(404)
			return
		}
		switch {
		case r.Method == "GET" && parts[3] == "json":
			json.NewEncoder(w).Encode(i)
		case r.Method == "POST" && parts[3] == "kill":
			signal := r.URL.Query().Get("signal")
			f.signals = append(f.signals, i.ID+":"+signal)
			if !f.stubborn || signal == "SIGKILL" {
				i.State.Running = false
				f.items[i.ID] = i
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(400)
		}
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return &Client{Resolve: func(context.Context) (string, error) { return "unix://" + path, nil }, Grace: 15 * time.Millisecond, Poll: time.Millisecond}
}

func TestDiscoveryComposeAndPublishedPorts(t *testing.T) {
	i := fixtureInfo("a", "shop-api-1", "shop")
	// Exposed but unpublished ports must not be presented as host ports.
	if err := json.Unmarshal([]byte(`{"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"8080"},{"HostIp":"::1","HostPort":"8080"}],"53/udp":[{"HostIp":"0.0.0.0","HostPort":"5354"}],"9000/tcp":null}}}`), &i); err != nil {
		t.Fatal(err)
	}
	j := fixtureInfo("b", "shop-worker-1", "shop")
	f := &fakeEngine{items: map[string]inspection{i.ID: i, j.ID: j}}
	c := serveEngine(t, f)
	items, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	p := items[0]
	if p.PID != 0 || p.Container == nil || p.Project.Name != "shop" || p.Project.Root != "/work/shop" || p.Container.Service != "api" {
		t.Fatalf("bad attribution: %+v", p)
	}
	if len(p.Sockets) != 3 || len(p.Container.Ports) != 3 {
		t.Fatalf("bad mappings: %+v", p.Container.Ports)
	}
	if p.Container.Ports[0].Protocol != "udp" || p.Container.Ports[1].ContainerPort != 80 || p.Container.Ports[2].Address != "::1" {
		t.Fatalf("bad protocol, ordering or translation: %+v", p.Container.Ports)
	}
	if len(items[1].Sockets) != 0 {
		t.Fatal("internal service invented a host socket")
	}
	if items[0].Key() == items[1].Key() {
		t.Fatal("containers without host PIDs must have distinct identities")
	}
}

func TestContainerStopIsolationAndExplicitEscalation(t *testing.T) {
	i, j := fixtureInfo("a", "api", "one"), fixtureInfo("b", "db", "two")
	f := &fakeEngine{items: map[string]inspection{i.ID: i, j.ID: j}, stubborn: true}
	c := serveEngine(t, f)
	endpoint, _ := c.Resolve(context.Background())
	p := workload(endpoint, i)
	r := c.Terminate(context.Background(), []model.Process{p}, false)
	if len(r) != 1 || r[0].Status != "running" {
		t.Fatalf("%+v", r)
	}
	f.mu.Lock()
	if len(f.signals) != 1 || f.signals[0] != i.ID+":SIGTERM" {
		t.Error("automatic escalation or wrong target", f.signals)
	}
	f.mu.Unlock()
	r = c.Terminate(context.Background(), []model.Process{p}, true)
	if r[0].Status != "exited" {
		t.Fatalf("%+v", r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items[i.ID].State.Running || !f.items[j.ID].State.Running || len(f.signals) != 2 || f.signals[1] != i.ID+":SIGKILL" {
		t.Fatal("wrong container affected or missing explicit KILL", f.signals)
	}
}

func TestRefusesRestartedMissingAndProtectedContainers(t *testing.T) {
	for _, mode := range []string{"restarted", "missing", "paused", "host network", "denied", "invalid ID", "remote", "protected"} {
		t.Run(mode, func(t *testing.T) {
			i := fixtureInfo("a", "api", "one")
			f := &fakeEngine{items: map[string]inspection{i.ID: i}}
			c := serveEngine(t, f)
			endpoint, _ := c.Resolve(context.Background())
			p := workload(endpoint, i)
			switch mode {
			case "restarted":
				i.State.StartedAt = "2026-10-15T10:01:00Z"
				f.items[i.ID] = i
			case "missing":
				delete(f.items, i.ID)
			case "paused":
				i.State.Paused = true
				f.items[i.ID] = i
			case "host network":
				i.HostConfig.NetworkMode = "host"
				f.items[i.ID] = i
			case "denied":
				f.denied = true
			case "invalid ID":
				p.Container.ID = "api"
			case "remote":
				p.Container.Endpoint = "tcp://production:2375"
			case "protected":
				p.Protected = "protected"
			}
			r := c.Terminate(context.Background(), []model.Process{p}, true)
			want := "refused"
			if mode == "missing" {
				want = "exited"
			}
			if r[0].Status != want {
				t.Fatalf("%+v", r)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.signals) != 0 {
				t.Fatal("unsafe signal", f.signals)
			}
		})
	}
}

func TestDiscoveryUnavailableAndRemoteContexts(t *testing.T) {
	for _, endpoint := range []string{"tcp://localhost:2375", "ssh://host", "http://localhost", "unix://other/var/run/docker.sock", "unix:relative", "unix:///tmp/sock?remote=true"} {
		if _, err := validateEndpoint(endpoint); err == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
	c := serveEngine(t, &fakeEngine{denied: true})
	if _, err := c.Discover(context.Background()); err == nil {
		t.Fatal("permission failure was hidden")
	}
	t.Setenv("DOCKER_HOST", "ssh://production")
	t.Setenv("DOCKER_CONTEXT", "")
	if _, err := localEndpoint(context.Background()); err == nil {
		t.Fatal("remote environment accepted")
	}
}

func TestRestartAfterTermPreventsForce(t *testing.T) {
	i := fixtureInfo("a", "api", "shop")
	f := &fakeEngine{items: map[string]inspection{i.ID: i}, stubborn: true}
	c := serveEngine(t, f)
	endpoint, _ := c.Resolve(context.Background())
	p := workload(endpoint, i)
	r := c.Terminate(context.Background(), []model.Process{p}, false)
	if r[0].Status != "running" {
		t.Fatal(r)
	}
	f.mu.Lock()
	i.State.StartedAt = "2026-10-15T10:01:00Z"
	f.items[i.ID] = i
	f.mu.Unlock()
	r = c.Terminate(context.Background(), []model.Process{r[0].Process}, true)
	if r[0].Status != "refused" {
		t.Fatal(r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.signals) != 1 {
		t.Fatal("force targeted a restarted container")
	}
}
