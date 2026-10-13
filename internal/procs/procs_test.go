package procs

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeProc(t *testing.T, procs map[string]string) string {
	root := t.TempDir()
	for pid, cmd := range procs {
		os.MkdirAll(filepath.Join(root, pid), 0o755)
		os.WriteFile(filepath.Join(root, pid, "cmdline"), []byte(cmd), 0o644)
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

func TestNoWikiJSProcessIsAnError(t *testing.T) {
	s := Stopper{ProcRoot: fakeProc(t, map[string]string{"7": "/wikijs-bootstrap\x00"}), Self: 7, Signal: func(int) error { return nil }}
	if s.StopApp() == nil {
		t.Error("stopped nothing without saying so")
	}
}
