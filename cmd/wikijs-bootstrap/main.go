// The Wiki.js bootstrap: runs once per container start, then stays up and
// reports on /healthz, which its Docker healthcheck reads. See the spec in
// paisans-stack, docs/specs/2026-10-13-wikijs-blueprint.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/converge"
	"github.com/paisans-software/wikijs-bootstrap/internal/procs"
	"github.com/paisans-software/wikijs-bootstrap/internal/state"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

type status struct {
	mu   sync.Mutex
	done bool
	err  error
}

func (s *status) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done, s.err = err == nil, err
}

func (s *status) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.done:
		fmt.Fprintln(w, "converged")
	case s.err != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, s.err)
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "converging")
	}
}

// dbState adapts state.DB to converge.State: Lock returns the interface, and
// returns it nil on error, not a typed nil pointer inside it.
type dbState struct{ state.DB }

func (d dbState) Lock(ctx context.Context) (converge.Held, error) {
	h, err := d.DB.Lock(ctx)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// healthURL is the address the healthcheck dials for a listen address: a
// wildcard or empty host means this machine.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("HEALTH_ADDR")
		if addr == "" {
			addr = ":8090"
		}
		u, err := healthURL(addr)
		if err != nil {
			os.Exit(1)
		}
		resp, err := (&http.Client{Timeout: 4 * time.Second}).Get(u)
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	c, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	s := &status{}
	go func() { log.Fatal(http.ListenAndServe(c.HealthAddr, s)) }()

	// Five minutes, not thirty seconds: /finalize runs Wiki.js's whole setup
	// and must not be cut short.
	w := &wiki.Client{BaseURL: c.WikiURL, HTTP: &http.Client{Timeout: 5 * time.Minute}, Log: log.Printf}
	db := dbState{state.DB{DSN: c.PgxDSN()}}
	for {
		err := converge.Run(context.Background(), c, w, db, procs.New(), 2*time.Second)
		s.set(err)
		if err == nil {
			log.Print("converged")
			select {}
		}
		if errors.Is(err, converge.ErrStopped) {
			log.Print(err)
			select {} // the namespace ends with the app; this container restarts with it
		}
		log.Printf("run failed, retrying in a minute: %v", err)
		time.Sleep(time.Minute)
	}
}
