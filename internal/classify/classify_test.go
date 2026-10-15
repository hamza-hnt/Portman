package classify

import (
	"portman/internal/model"
	"testing"
)

func TestCategory(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		uid, ppid  int
		project    bool
		want       model.Category
	}{
		{"node", "/opt/homebrew/bin/node", 501, 50, true, model.Development},
		{"Python", "/opt/homebrew/Cellar/python@3.14/3.14.7/Frameworks/Python.framework/Versions/3.14/Resources/Python.app/Contents/MacOS/Python", 501, 50, true, model.Development},
		{"postgres", "/opt/homebrew/Cellar/postgresql/17/bin/postgres", 501, 1, false, model.Homebrew},
		{"node", "/opt/homebrew/bin/node", 501, 40, false, model.Application},
		{"com.docker.backend", "/Applications/Docker.app/Contents/MacOS/backend", 501, 1, true, model.Docker},
		{"chrome", "/Applications/Chrome.app/Contents/MacOS/Chrome", 501, 40, true, model.Application},
		{"ControlCenter", "/System/Library/CoreServices/ControlCenter.app/Contents/MacOS/ControlCenter", 501, 1, false, model.System},
		{"daemon", "/bin/tool", 0, 1, true, model.System},
		{"systemd", "/usr/lib/systemd/systemd", 1000, 1, false, model.System},
		{"node", "/usr/bin/node", 1000, 40, true, model.Development},
		{"redis-server", "/home/linuxbrew/.linuxbrew/Cellar/redis/bin/redis-server", 1000, 1, false, model.Homebrew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := model.Process{Identity: model.Identity{PID: 42}, Name: tc.name, Executable: tc.path, UID: tc.uid, PPID: tc.ppid}
			if tc.project {
				p.Project = &model.Project{Name: "project"}
			}
			if got := Category(p); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
