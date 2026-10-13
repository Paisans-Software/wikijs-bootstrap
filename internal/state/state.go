// Package state reads what the bootstrap needs from Wiki.js's database and
// holds the lock that lets one instance's bootstrap at a time set it up. It
// never writes to the database.
package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LockKey is the advisory lock every bootstrap against one database takes.
const LockKey int64 = 0x57494b49424f4f54

type DB struct{ DSN string }

// Held is the advisory lock, held on its own connection until Release.
type Held struct{ conn *pgx.Conn }

// Check is nil while the lock's connection is alive, so the lock is still held.
func (h *Held) Check(ctx context.Context) error {
	if _, err := h.conn.Exec(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("the lock's connection is gone: %w", err)
	}
	return nil
}

// Release unlocks and closes the connection, giving up after five seconds.
// Closing the connection releases the lock even if the unlock is never sent,
// so an error from the unlock is ignored.
func (h *Held) Release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", LockKey)
	h.conn.Close(ctx)
}

// Lock blocks until this session holds the advisory lock, or ctx ends. The
// lock is a session lock on its own connection, released by Release or by
// the connection closing. It needs a direct or session-mode connection (the
// toolkit's HAProxy is TCP passthrough, which is fine); a transaction-mode
// pooler would not keep it on one backend. The connection uses the simple
// protocol for the same reason.
func (d DB) Lock(ctx context.Context) (*Held, error) {
	cfg, err := pgx.ParseConfig(d.DSN)
	if err != nil {
		return nil, errors.New("the database DSN does not parse")
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting for the lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", LockKey); err != nil {
		conn.Close(context.Background())
		return nil, fmt.Errorf("taking the lock: %w", err)
	}
	return &Held{conn: conn}, nil
}

// SettingsPopulated is whether Wiki.js's settings table exists and has a
// row: whether setup has run against this database.
func (d DB) SettingsPopulated(ctx context.Context) (bool, error) {
	conn, err := pgx.Connect(ctx, d.DSN)
	if err != nil {
		return false, err
	}
	defer conn.Close(context.Background())
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('settings') IS NOT NULL").Scan(&exists); err != nil || !exists {
		return false, err
	}
	var any bool
	err = conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM settings)").Scan(&any)
	return any, err
}
