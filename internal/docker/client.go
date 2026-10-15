// Package docker discovers and signals containers through a local Unix socket.
// It never treats a container PID (which may belong to a VM) as a host PID.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"portman/internal/model"
)

var errMissing = errors.New("container no longer exists")
var fullID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Client struct {
	// Resolve is injectable for tests. An empty endpoint disables discovery.
	Resolve func(context.Context) (string, error)
	Grace   time.Duration
	Poll    time.Duration
}

func New() *Client {
	return &Client{Resolve: localEndpoint, Grace: 2 * time.Second, Poll: 100 * time.Millisecond}
}

func validateEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "unix" || u.Host != "" || !filepath.IsAbs(u.Path) || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("only local Docker Unix sockets are supported; remote contexts are ignored")
	}
	return u.Path, nil
}

func localEndpoint(ctx context.Context) (string, error) {
	// DOCKER_CONTEXT takes precedence over DOCKER_HOST, as in Docker's CLI.
	if host := os.Getenv("DOCKER_HOST"); host != "" && os.Getenv("DOCKER_CONTEXT") == "" {
		_, err := validateEndpoint(host)
		return host, err
	}
	cli, err := exec.LookPath("docker")
	if err != nil {
		cli = "/Applications/Docker.app/Contents/Resources/bin/docker"
		if _, err := os.Stat(cli); err != nil {
			if os.Getenv("DOCKER_CONTEXT") != "" {
				return "", errors.New("Docker CLI is needed to resolve DOCKER_CONTEXT")
			}
			if _, err := os.Stat("/var/run/docker.sock"); err == nil {
				return "unix:///var/run/docker.sock", nil
			}
			return "", nil
		}
	}
	output, err := exec.CommandContext(ctx, cli, "context", "inspect", "--format", `{{json .Endpoints.docker.Host}}`).Output()
	if err != nil {
		return "", errors.New("cannot resolve local Docker context")
	}
	var endpoint string
	if err := json.Unmarshal(output, &endpoint); err != nil {
		return "", errors.New("invalid Docker context endpoint")
	}
	_, err = validateEndpoint(endpoint)
	return endpoint, err
}

type api struct {
	http      *http.Client
	transport *http.Transport
}

func connect(endpoint string) (*api, error) {
	path, err := validateEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	return &api{&http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport}, nil
}

// Unversioned Engine endpoints use the daemon's current API. Only stable
// fields are decoded; environment variables and mount contents are never used.
func (a *api) request(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return errors.New("local Docker engine unavailable or socket access denied")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errMissing
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Docker %s failed (HTTP %d)", method, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
	}
	return nil
}

type inspection struct {
	ID    string `json:"Id"`
	Name  string
	State struct {
		Running, Paused, Restarting bool
		StartedAt                   string
	}
	Config struct {
		Image  string
		Labels map[string]string
	}
	HostConfig      struct{ NetworkMode string }
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func (a *api) inspect(ctx context.Context, id string) (inspection, error) {
	var result inspection
	if !fullID.MatchString(id) {
		return result, errors.New("full container identity required")
	}
	err := a.request(ctx, http.MethodGet, "/containers/"+id+"/json", &result)
	if err == nil && result.ID != id {
		err = errors.New("container identity changed")
	}
	return result, err
}

func (c *Client) Discover(ctx context.Context) ([]model.Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	endpoint, err := c.Resolve(ctx)
	if err != nil || endpoint == "" {
		return nil, err
	}
	a, err := connect(endpoint)
	if err != nil {
		return nil, err
	}
	defer a.transport.CloseIdleConnections()
	var entries []struct {
		ID string `json:"Id"`
	}
	if err := a.request(ctx, http.MethodGet, "/containers/json", &entries); err != nil {
		return nil, err
	}
	result := []model.Process{}
	for _, entry := range entries {
		info, err := a.inspect(ctx, entry.ID)
		if errors.Is(err, errMissing) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.State.Running {
			continue
		}
		p := workload(endpoint, info)
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func workload(endpoint string, info inspection) model.Process {
	labels := info.Config.Labels
	c := &model.Container{ID: info.ID, StartedAt: info.State.StartedAt, Endpoint: endpoint, Image: info.Config.Image, ComposeProject: labels["com.docker.compose.project"], Service: labels["com.docker.compose.service"]}
	p := model.Process{Name: strings.TrimPrefix(info.Name, "/"), UID: -1, Category: model.Docker, Container: c, CWD: labels["com.docker.compose.project.working_dir"]}
	if c.ComposeProject != "" {
		p.Project = &model.Project{Name: c.ComposeProject, Root: p.CWD, Marker: "compose", Source: "Docker Compose labels"}
	}
	if info.State.Paused {
		p.Protected = "paused container; unpause it in Docker first"
	}
	if info.State.Restarting {
		p.Protected = "container is restarting; refresh before stopping"
	}
	if info.State.StartedAt == "" || strings.HasPrefix(info.State.StartedAt, "0001-") {
		p.Protected = "container start identity unavailable"
	}
	if info.HostConfig.NetworkMode == "host" {
		p.Protected = "host network container; ports cannot be isolated"
	}
	for port, bindings := range info.NetworkSettings.Ports {
		parts := strings.Split(port, "/")
		if len(parts) != 2 || (parts[1] != "tcp" && parts[1] != "udp") {
			continue
		}
		inside, err := strconv.Atoi(parts[0])
		if err != nil || inside < 1 || inside > 65535 {
			continue
		}
		for _, binding := range bindings {
			outside, err := strconv.Atoi(binding.HostPort)
			if err != nil || outside < 1 || outside > 65535 {
				continue
			}
			c.Ports = append(c.Ports, model.PortMapping{Address: binding.HostIP, HostPort: outside, ContainerPort: inside, Protocol: parts[1]})
		}
	}
	sort.Slice(c.Ports, func(i, j int) bool {
		a, b := c.Ports[i], c.Ports[j]
		if a.HostPort != b.HostPort {
			return a.HostPort < b.HostPort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.ContainerPort < b.ContainerPort
	})
	for _, port := range c.Ports {
		family, state := "IPv4", "LISTEN"
		if strings.Contains(port.Address, ":") {
			family = "IPv6"
		}
		if port.Protocol == "udp" {
			state = "BOUND"
		}
		p.Sockets = append(p.Sockets, model.Socket{Protocol: strings.ToUpper(port.Protocol), Family: family, Address: port.Address, Port: port.HostPort, State: state})
	}
	return p
}
