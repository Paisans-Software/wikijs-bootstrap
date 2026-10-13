// Package procs stops the Wiki.js process the bootstrap shares a process
// namespace with (compose: pid: "service:app"). Wiki.js shuts down on
// SIGTERM (server/index.js:46 at v2.5.316) and its restart policy brings it
// back, reading the database afresh.
package procs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type Stopper struct {
	ProcRoot string
	Self     int
	Signal   func(pid int) error
}

func New() Stopper {
	return Stopper{ProcRoot: "/proc", Self: os.Getpid(), Signal: func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }}
}

// StopApp signals the one process running `node ... server`, the image's
// command (dev/build/Dockerfile:56).
func (s Stopper) StopApp() error {
	entries, err := os.ReadDir(s.ProcRoot)
	if err != nil {
		return err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == s.Self {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join(s.ProcRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(cmd, "\x00"), []byte{0})
		if len(args) >= 2 && filepath.Base(string(args[0])) == "node" && string(args[len(args)-1]) == "server" {
			return s.Signal(pid)
		}
	}
	return fmt.Errorf("no Wiki.js process in %s: is pid: service:app set on the bootstrap?", s.ProcRoot)
}
