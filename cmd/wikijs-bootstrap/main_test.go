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
