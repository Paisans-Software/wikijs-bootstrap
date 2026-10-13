package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthSaysConvergingThenConvergedOrTheError(t *testing.T) {
	var s status
	check := func(code int, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != code || !strings.Contains(rec.Body.String(), body) {
			t.Errorf("%d %q, want %d %q", rec.Code, rec.Body.String(), code, body)
		}
	}
	check(503, "converging")
	s.set(errors.New("the break glass admin could not log in"))
	check(503, "break glass")
	s.set(nil)
	check(200, "converged")
}

// A failed Lock must hand converge a nil interface: a typed nil *state.Held
// inside a converge.Held would compare non-nil and be called.
func TestLockFailureReturnsANilInterface(t *testing.T) {
	h, err := dbState{}.Lock(context.Background())
	if err == nil {
		t.Fatal("an empty DSN took a lock")
	}
	if h != nil {
		t.Errorf("Lock returned %#v with its error, want a nil interface", h)
	}
}

func TestHealthURL(t *testing.T) {
	for addr, want := range map[string]string{
		":8090":          "http://127.0.0.1:8090/healthz",
		"0.0.0.0:8090":   "http://127.0.0.1:8090/healthz",
		"127.0.0.1:8090": "http://127.0.0.1:8090/healthz",
		"[::]:8090":      "http://127.0.0.1:8090/healthz",
		"[::1]:8090":     "http://[::1]:8090/healthz",
	} {
		if got, err := healthURL(addr); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", addr, got, err, want)
		}
	}
	if got, err := healthURL("8090"); err == nil {
		t.Errorf("8090 gave %q, want an error", got)
	}
}
