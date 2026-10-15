package platform

import (
	"strings"
	"testing"
)

func TestProcStatNamesAndIdentity(t *testing.T) {
	line := "42 (worker ) (with\nspaces)) S 9 " + strings.Repeat("0 ", 17) + "12345 0"
	p, err := parseProcStat([]byte(line), 42)
	if err != nil || p.Name != "worker ) (with\nspaces)" || p.PPID != 9 || p.StartID != 12345 || p.Zombie {
		t.Fatalf("%+v / %v", p, err)
	}
	line = strings.Replace(line, ") S 9", ") Z 9", 1)
	p, err = parseProcStat([]byte(line), 42)
	if err != nil || !p.Zombie {
		t.Fatalf("%+v / %v", p, err)
	}
	for _, data := range []string{"", "42 x", "42 (x) S 1", strings.Replace(line, "12345", "invalid", 1)} {
		if _, err := parseProcStat([]byte(data), 42); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	if _, err := parseProcStat([]byte(line), 43); err == nil {
		t.Fatal("accepted mismatched PID")
	}
}
