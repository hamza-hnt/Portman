// Package project resolves project roots from working directories and ancestry.
package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"portman/internal/classify"
	"portman/internal/model"
)

var markers = []string{"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "pom.xml", "build.gradle", "build.gradle.kts", "Gemfile", "composer.json", "mix.exs", "CMakeLists.txt", ".git"}

// Resolver is scoped to one scan. This caches repeated filesystem walks without
// retaining stale roots across refreshes. It is used by a single scan goroutine.
type Resolver struct {
	Inspector model.Inspector
	roots     map[string]*model.Project
	parents   map[int]model.Process
}

func New(inspector model.Inspector) *Resolver {
	return &Resolver{Inspector: inspector, roots: map[string]*model.Project{}, parents: map[int]model.Process{}}
}

func (r *Resolver) Resolve(ctx context.Context, p model.Process) *model.Project {
	seen := map[int]bool{}
	for depth := 0; depth < 12 && p.PID > 1 && !seen[p.PID]; depth++ {
		if ctx.Err() != nil {
			return nil
		}
		seen[p.PID] = true
		// Desktop apps often inherit the shell's directory; do not attach an
		// application or a system daemon to the project that launched it.
		if classify.IsDesktopApp(p.Executable) || strings.HasPrefix(p.Executable, "/System/") {
			return nil
		}
		if root := r.root(p.CWD); root != nil {
			copy := *root
			copy.Source = "working directory"
			if depth > 0 {
				copy.Source = "parent working directory"
			}
			return &copy
		}
		if r.Inspector == nil || p.PPID <= 1 {
			break
		}
		parent, ok := r.parents[p.PPID]
		if !ok {
			var err error
			parent, err = r.Inspector.Inspect(ctx, p.PPID)
			if err != nil {
				break
			}
			r.parents[p.PPID] = parent
		}
		if parent.UID != p.UID || parent.StartID > p.StartID {
			break
		}
		p = parent
	}
	return nil
}

func (r *Resolver) root(dir string) *model.Project {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if value, ok := r.roots[dir]; ok {
		return value
	}
	original := dir
	defer func() {
		if _, ok := r.roots[original]; !ok {
			r.roots[original] = nil
		}
	}()
	home, _ := os.UserHomeDir()
	var nearest *model.Project
	for depth := 0; depth < 40 && dir != "/" && dir != "." && dir != home; depth++ {
		// Group monorepo services under the nearest repository boundary, while
		// keeping the nearest manifest for projects with no .git directory.
		for _, marker := range markers {
			info, err := os.Stat(filepath.Join(dir, marker))
			if err != nil || (marker != ".git" && !info.Mode().IsRegular()) {
				continue
			}
			if nearest == nil {
				nearest = &model.Project{Name: projectName(dir), Root: dir, Marker: marker}
			}
			if marker == ".git" {
				nearest = &model.Project{Name: projectName(dir), Root: dir, Marker: marker}
				r.roots[original] = nearest
				return nearest
			}
		}
		dir = filepath.Dir(dir)
	}
	r.roots[original] = nearest
	return nearest
}

func projectName(dir string) string {
	// Package names improve labels, but roots (not names) define identity.
	path := filepath.Join(dir, "package.json")
	if info, err := os.Stat(path); err == nil && info.Size() < 64*1024 {
		if data, err := os.ReadFile(path); err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &pkg) == nil && strings.TrimSpace(pkg.Name) != "" {
				return pkg.Name
			}
		}
	}
	return filepath.Base(dir)
}
