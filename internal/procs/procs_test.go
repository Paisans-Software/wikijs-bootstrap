package procs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func fakeProc(t *testing.T, procs map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, cmd := range procs {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pid, "cmdline"), []byte(cmd), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestStopsTheWikiJSProcessOnly(t *testing.T) {
	root := fakeProc(t, map[string]string{
		"1":  "node\x00--no-deprecation\x00server\x00",
		"7":  "/wikijs-bootstrap\x00",
		"12": "sh\x00-c\x00sleep 1\x00",
	})
	var signalled []int
	s := Stopper{ProcRoot: root, Self: 7, Signal: func(pid int) error { signalled = append(signalled, pid); return nil }}
	if err := s.StopApp(); err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 1 || signalled[0] != 1 {
		t.Errorf("signalled %v", signalled)
	}
}

func TestSignalsLowestPidWhenTwoMatch(t *testing.T) {
	root := fakeProc(t, map[string]string{
		"12": "node\x00--no-deprecation\x00server\x00",
		"3":  "node\x00--no-deprecation\x00server\x00",
		"7":  "/wikijs-bootstrap\x00",
	})
	var signalled []int
	s := Stopper{ProcRoot: root, Self: 7, Signal: func(pid int) error { signalled = append(signalled, pid); return nil }}
	if err := s.StopApp(); err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 1 || signalled[0] != 3 {
		t.Errorf("signalled %v, want [3]", signalled)
	}
}

func TestNoWikiJSProcessIsAnError(t *testing.T) {
	s := Stopper{ProcRoot: fakeProc(t, map[string]string{"7": "/wikijs-bootstrap\x00"}), Self: 7, Signal: func(int) error { return nil }}
	if s.StopApp() == nil {
		t.Error("stopped nothing without saying so")
	}
}

func TestStoppingLogsThePidAndResult(t *testing.T) {
	root := fakeProc(t, map[string]string{"42": "node\x00server\x00", "7": "/wikijs-bootstrap\x00"})
	for _, tc := range []struct {
		signal error
		want   string
	}{
		{nil, "write SIGTERM Wiki.js pid 42: ok"},
		{errors.New("operation not permitted"), "write SIGTERM Wiki.js pid 42: operation not permitted"},
	} {
		var lines []string
		s := Stopper{ProcRoot: root, Self: 7,
			Signal: func(int) error { return tc.signal },
			Log:    func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
		if err := s.StopApp(); !errors.Is(err, tc.signal) {
			t.Fatalf("StopApp() = %v, want %v", err, tc.signal)
		}
		if len(lines) != 1 || lines[0] != tc.want {
			t.Errorf("logged %q, want one line %q", lines, tc.want)
		}
	}
}
