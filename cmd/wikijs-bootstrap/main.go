// The Wiki.js bootstrap: runs once per container start, then stays up,
// polls Wiki.js for drift and reports on /livez, which its Docker
// healthcheck reads, on /healthz and to its monitors. See the specs in
// paisans-cli, docs/specs/2026-10-13-wikijs-blueprint.md and
// docs/specs/2026-10-14-wikijs-groups-and-drift.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/wikijs-bootstrap/internal/config"
	"github.com/paisans-software/wikijs-bootstrap/internal/converge"
	"github.com/paisans-software/wikijs-bootstrap/internal/heartbeat"
	"github.com/paisans-software/wikijs-bootstrap/internal/procs"
	"github.com/paisans-software/wikijs-bootstrap/internal/state"
	"github.com/paisans-software/wikijs-bootstrap/internal/wiki"
)

// version is the release, set at build time.
var version = "dev"

// status is the last run's result and the last drift poll's.
type status struct {
	mu        sync.Mutex
	converged bool
	runErr    error
	drift     []string
	pollErr   error
}

func (s *status) ran(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.converged, s.runErr = err == nil, err
}

func (s *status) polled(drift []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drift, s.pollErr = drift, err
}

// live is /livez, Docker's health: whether a run has converged. What a later
// poll finds does not change it.
func (s *status) live() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveLocked()
}

func (s *status) liveLocked() (int, string) {
	switch {
	case s.converged:
		return http.StatusOK, "converged"
	case s.runErr != nil:
		return http.StatusServiceUnavailable, s.runErr.Error()
	}
	return http.StatusServiceUnavailable, "converging"
}

// health is /healthz: converged, and the last poll found Wiki.js holding
// what the configuration says.
func (s *status) health() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.converged:
		return s.liveLocked()
	case s.pollErr != nil:
		return http.StatusServiceUnavailable, "drift poll failed: " + s.pollErr.Error()
	case len(s.drift) > 0:
		return http.StatusServiceUnavailable, "drifted: " + strings.Join(s.drift, "; ")
	}
	return http.StatusOK, "converged"
}

// beat is what the heartbeat pushes: nothing while a run is in progress,
// otherwise up or down as /healthz is, with its body.
func (s *status) beat() (heartbeat.State, string) {
	s.mu.Lock()
	running := !s.converged && s.runErr == nil
	s.mu.Unlock()
	if running {
		return heartbeat.None, ""
	}
	code, body := s.health()
	if code == http.StatusOK {
		return heartbeat.Up, body
	}
	return heartbeat.Down, body
}

func (s *status) handler() http.Handler {
	serve := func(f func() (int, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			code, body := f()
			w.WriteHeader(code)
			fmt.Fprintln(w, body)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/livez", serve(s.live))
	mux.Handle("/healthz", serve(s.health))
	return mux
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

// livezURL is the address the healthcheck dials for a listen address: a
// wildcard or empty host means this machine.
func livezURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/livez", nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("HEALTH_ADDR")
		if addr == "" {
			addr = ":8090"
		}
		u, err := livezURL(addr)
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
	go func() { log.Fatal(http.ListenAndServe(c.HealthAddr, s.handler())) }()
	if len(c.HeartbeatURLs) > 0 {
		go beat(s, &heartbeat.Pusher{
			URLs: c.HeartbeatURLs, HTTP: &http.Client{Timeout: 10 * time.Second},
			UserAgent: "wikijs-bootstrap/" + version, Wait: 2 * time.Second, Log: log.Printf,
		})
	}

	// Five minutes, not thirty seconds: /finalize runs Wiki.js's whole setup
	// and must not be cut short.
	w := &wiki.Client{BaseURL: c.WikiURL, HTTP: &http.Client{Timeout: 5 * time.Minute}, Log: log.Printf}
	db := dbState{state.DB{DSN: c.PgxDSN()}}
	for {
		err := converge.Run(context.Background(), c, w, db, procs.New(log.Printf), 2*time.Second)
		s.ran(err)
		if err == nil {
			log.Print("converged")
			poll(c, &wiki.Client{BaseURL: c.WikiURL, HTTP: &http.Client{Timeout: 30 * time.Second}}, s)
		}
		if errors.Is(err, converge.ErrStopped) {
			log.Print(err)
			select {} // the namespace ends with the app; this container restarts with it
		}
		log.Printf("run failed, retrying in a minute: %v", err)
		time.Sleep(time.Minute)
	}
}

// poll compares Wiki.js with the configuration every DRIFT_INTERVAL, for
// good: one log line per difference, and the result on /healthz.
func poll(c config.Config, r converge.Reader, s *status) {
	clean := true
	for range time.Tick(c.DriftInterval) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		drift, err := converge.Drift(ctx, c, r)
		cancel()
		s.polled(drift, err)
		switch {
		case err != nil:
			log.Printf("drift poll failed: %v", err)
		case len(drift) > 0:
			for _, d := range drift {
				log.Printf("drift: %s", d)
			}
		case !clean:
			log.Print("drift poll: Wiki.js holds the configuration again")
		}
		clean = err == nil && len(drift) == 0
	}
}

// beat pushes the last result to the monitors every minute, for good.
func beat(s *status, p *heartbeat.Pusher) {
	for range time.Tick(time.Minute) {
		state, body := s.beat()
		p.Push(context.Background(), state, body)
	}
}
