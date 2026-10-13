//go:build docker

package state

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A throwaway Postgres on a random port, removed after the test.
func postgres(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "run", "-d", "--rm", "-e", "POSTGRES_PASSWORD=test-only", "-p", "127.0.0.1::5432", "postgres:18").Output()
	if err != nil {
		t.Skip("docker unavailable")
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	port, _ := exec.Command("docker", "port", id, "5432").Output()
	dsn := "postgresql://postgres:test-only@" + strings.TrimSpace(string(port)) + "/postgres?sslmode=disable"
	for i := 0; i < 60; i++ {
		if _, err := (DB{DSN: dsn}).SettingsPopulated(context.Background()); err == nil {
			return dsn
		}
		time.Sleep(time.Second)
	}
	t.Fatal("postgres did not start")
	return ""
}

func TestNoSettingsTableIsNotPopulated(t *testing.T) {
	d := DB{DSN: postgres(t)}
	if got, err := d.SettingsPopulated(context.Background()); err != nil || got {
		t.Errorf("populated = %v, %v", got, err)
	}
}

func TestTheLockExcludesASecondHolder(t *testing.T) {
	d := DB{DSN: postgres(t)}
	unlock, err := d.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := d.Lock(ctx); err == nil {
		t.Fatal("a second holder took the lock")
	}
	unlock()
	if u2, err := d.Lock(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		u2()
	}
}
