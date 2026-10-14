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
	// Log receives the one line every write is logged on: what, the target
	// and the result. Nil logs nothing.
	Log func(format string, args ...any)
}

func New(log func(format string, args ...any)) Stopper {
	return Stopper{ProcRoot: "/proc", Self: os.Getpid(), Signal: func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }, Log: log}
}

// StopApp signals the one process running `node ... server`, the image's
// command (dev/build/Dockerfile:56).
//
// Two matches are possible (a forked child, or a restart overlap). Only the
// lowest pid is signalled: that is the container's own Wiki.js process, and
// stopping it ends the container, which its restart policy brings back. The
// /proc listing is lexical ("10" sorts before "2"), so the lowest pid is
// chosen numerically, never by the order entries arrive in.
func (s Stopper) StopApp() error {
	entries, err := os.ReadDir(s.ProcRoot)
	if err != nil {
		return err
	}
	lowest := -1
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
			if lowest == -1 || pid < lowest {
				lowest = pid
			}
		}
	}
	if lowest == -1 {
		return fmt.Errorf("no Wiki.js process in %s: is pid: service:app set on the bootstrap?", s.ProcRoot)
	}
	err = s.Signal(lowest)
	if s.Log != nil {
		if err != nil {
			s.Log("write SIGTERM Wiki.js pid %d: %v", lowest, err)
		} else {
			s.Log("write SIGTERM Wiki.js pid %d: ok", lowest)
		}
	}
	return err
}
