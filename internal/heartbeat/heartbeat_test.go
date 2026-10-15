package heartbeat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type seen struct {
	method, path, contentType, userAgent, body string
}

type monitor struct {
	mu    sync.Mutex
	got   []seen
	fails int // answer 500 to this many requests first
	block chan struct{}
}

func (m *monitor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.block != nil {
		<-m.block
	}
	b, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.got = append(m.got, seen{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), string(b)})
	if m.fails > 0 {
		m.fails--
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func pusher(urls ...string) (*Pusher, *[]string) {
	var mu sync.Mutex
	var lines []string
	return &Pusher{URLs: urls, HTTP: &http.Client{Timeout: time.Second}, UserAgent: "wikijs-bootstrap/test", Wait: time.Millisecond,
		Log: func(format string, a ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, a...))
		}}, &lines
}

func TestUpIsAGet(t *testing.T) {
	m := &monitor{}
	s := httptest.NewServer(m)
	defer s.Close()
	p, lines := pusher(s.URL + "/ping/tok123")
	p.Push(context.Background(), Up, "converged\n")
	if len(m.got) != 1 || m.got[0].method != http.MethodGet || m.got[0].path != "/ping/tok123" || m.got[0].userAgent != "wikijs-bootstrap/test" {
		t.Errorf("%+v", m.got)
	}
	if len(*lines) != 0 {
		t.Errorf("logged %q", *lines)
	}
}

func TestDownIsAPostToFailWithTheBody(t *testing.T) {
	m := &monitor{}
	s := httptest.NewServer(m)
	defer s.Close()
	p, _ := pusher(s.URL + "/ping/tok123")
	p.Push(context.Background(), Down, "drifted: site title: Wiki.js has \"A\", configuration says \"B\"\n")
	if len(m.got) != 1 {
		t.Fatalf("%+v", m.got)
	}
	g := m.got[0]
	if g.method != http.MethodPost || g.path != "/ping/tok123/fail" || g.contentType != "text/plain" || !strings.HasPrefix(g.body, "drifted: site title") {
		t.Errorf("%+v", g)
	}
}

func TestNoneSendsNothing(t *testing.T) {
	m := &monitor{}
	s := httptest.NewServer(m)
	defer s.Close()
	p, _ := pusher(s.URL + "/ping/tok123")
	p.Push(context.Background(), None, "converging\n")
	if len(m.got) != 0 {
		t.Errorf("%+v", m.got)
	}
}

func TestTwoRetriesThenOneLineNamingOnlyTheHost(t *testing.T) {
	m := &monitor{fails: 2}
	s := httptest.NewServer(m)
	defer s.Close()
	p, lines := pusher(s.URL + "/ping/tok123")
	p.Push(context.Background(), Up, "")
	if len(m.got) != 3 || len(*lines) != 0 {
		t.Errorf("tries %d, lines %q", len(m.got), *lines)
	}

	m = &monitor{fails: 10}
	s2 := httptest.NewServer(m)
	defer s2.Close()
	p, lines = pusher(s2.URL + "/ping/tok123")
	p.Push(context.Background(), Down, "x")
	if len(m.got) != 3 || len(*lines) != 1 {
		t.Fatalf("tries %d, lines %q", len(m.got), *lines)
	}
	host := strings.TrimPrefix(s2.URL, "http://")
	if !strings.Contains((*lines)[0], host) || strings.Contains((*lines)[0], "tok123") {
		t.Errorf("line %q", (*lines)[0])
	}
}

func TestAnUnreachableMonitorDoesNotLogItsToken(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL
	s.Close()
	p, lines := pusher(u + "/ping/tok123")
	p.Push(context.Background(), Up, "")
	if len(*lines) != 1 || strings.Contains((*lines)[0], "tok123") || !strings.Contains((*lines)[0], strings.TrimPrefix(u, "http://")) {
		t.Errorf("lines %q", *lines)
	}
}

func TestASlowMonitorDelaysNoOther(t *testing.T) {
	slow := &monitor{block: make(chan struct{})}
	fast := &monitor{}
	ss, fs := httptest.NewServer(slow), httptest.NewServer(fast)
	defer ss.Close()
	defer fs.Close()
	p, _ := pusher(ss.URL+"/ping/a", fs.URL+"/ping/b")
	done := make(chan struct{})
	go func() { p.Push(context.Background(), Up, ""); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		fast.mu.Lock()
		n := len(fast.got)
		fast.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fast monitor waited on the slow one")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("Push returned before the slow monitor answered")
	default:
	}
	close(slow.block)
	<-done
}
