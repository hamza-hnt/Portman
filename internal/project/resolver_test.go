package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"portman/internal/model"
)

func write(t *testing.T, root, path, content string) {
	t.Helper()
	target := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMonorepoAndStandalone(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	write(t, root, ".git", "gitdir: /tmp/linked-worktree")
	write(t, root, "package.json", `{"name":"workspace"}`)
	write(t, root, "apps/web/package.json", `{"name":"web"}`)
	r := New(nil)
	p := r.Resolve(context.Background(), model.Process{Identity: model.Identity{PID: 42}, CWD: filepath.Join(root, "apps/web")})
	if p == nil || p.Root != root || p.Name != "workspace" || p.Marker != ".git" {
		t.Fatalf("unexpected project: %+v", p)
	}
	other := t.TempDir()
	other, _ = filepath.EvalSymlinks(other)
	write(t, other, "go.mod", "module test")
	if p := New(nil).root(other); p == nil || p.Root != other || p.Marker != "go.mod" {
		t.Fatalf("unexpected standalone: %+v", p)
	}
}

type fakeInspector map[int]model.Process

func (f fakeInspector) Inspect(_ context.Context, pid int) (model.Process, error) { return f[pid], nil }

func TestParentResolutionAndBoundaries(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	write(t, root, "pom.xml", "<project/>")
	parent := model.Process{Identity: model.Identity{PID: 20, StartID: 1}, UID: 501, CWD: root}
	child := model.Process{Identity: model.Identity{PID: 30, StartID: 2}, PPID: 20, UID: 501}
	r := New(fakeInspector{20: parent})
	if p := r.Resolve(context.Background(), child); p == nil || p.Source != "parent working directory" {
		t.Fatalf("missing parent resolution: %+v", p)
	}
	parent.UID = 0
	if p := New(fakeInspector{20: parent}).Resolve(context.Background(), child); p != nil {
		t.Fatal("crossed owner boundary")
	}
	child.Executable = "/Applications/Editor.app/Contents/MacOS/editor"
	child.CWD = root
	if p := New(nil).Resolve(context.Background(), child); p != nil {
		t.Fatal("desktop app inherited project")
	}
	child.Executable = "/Library/Frameworks/Python.framework/Versions/3.14/Resources/Python.app/Contents/MacOS/Python"
	if p := New(nil).Resolve(context.Background(), child); p == nil {
		t.Fatal("framework Python must resolve development projects")
	}
	child.Executable = ""
	child.CWD = ""
	child.PPID = 30
	if p := New(fakeInspector{30: child}).Resolve(context.Background(), child); p != nil {
		t.Fatal("cycle should stop")
	}
}

func TestCacheOnlyLastsOneScan(t *testing.T) {
	root := t.TempDir()
	if New(nil).root(root) != nil {
		t.Fatal("unexpected project")
	}
	write(t, root, "pyproject.toml", "[project]")
	if New(nil).root(root) == nil {
		t.Fatal("new manifest must be found")
	}
}
