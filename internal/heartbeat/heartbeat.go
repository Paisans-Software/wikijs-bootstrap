// Package heartbeat pushes the bootstrap's last result to its monitors, each
// a push monitor's https://<monitor hostname>/ping/<token>. A URL carries
// its monitor's token, so nothing here logs one: a failure names the host.
package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// State is what a push says.
type State int

const (
	// None sends nothing: a run is in progress.
	None State = iota
	// Up is GET <url>: converged, and the last poll found no drift.
	Up
	// Down is POST <url>/fail with the /healthz body: drifted, or a run or
	// a poll failed.
	Down
)

// tries is one push and two retries.
const tries = 3

type Pusher struct {
	URLs      []string
	HTTP      *http.Client
	UserAgent string
	// Wait is the pause between tries.
	Wait time.Duration
	Log  func(format string, args ...any)
}

// Push sends s to every URL at once and returns when each has answered or
// used its tries. A push that fails every try is one log line.
func (p *Pusher) Push(ctx context.Context, s State, body string) {
	if s == None {
		return
	}
	var wg sync.WaitGroup
	for _, u := range p.URLs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			for i := 0; i < tries; i++ {
				if i > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(p.Wait):
					}
				}
				if err = p.once(ctx, u, s, body); err == nil {
					return
				}
			}
			p.Log("heartbeat to %s failed after %d tries: %v", host(u), tries, err)
		}()
	}
	wg.Wait()
}

func (p *Pusher) once(ctx context.Context, u string, s State, body string) error {
	method, target, payload := http.MethodGet, u, ""
	if s == Down {
		method, target, payload = http.MethodPost, u+"/fail", body
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(payload))
	if err != nil {
		return errors.New("the URL does not parse")
	}
	if s == Down {
		req.Header.Set("Content-Type", "text/plain")
	}
	req.Header.Set("User-Agent", p.UserAgent)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		// A *url.Error's message carries the URL, and with it the token.
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("answered %s", resp.Status)
	}
	return nil
}

func host(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return "a monitor whose URL does not parse"
}
