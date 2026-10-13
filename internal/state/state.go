// Package state reads what the bootstrap needs from Wiki.js's database and
// holds the lock that lets one instance's bootstrap at a time set it up. It
// never writes to the database.
package state

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LockKey is the advisory lock every bootstrap against one database takes.
const LockKey int64 = 0x57494b49424f4f54

type DB struct{ DSN string }

// Lock blocks until this session holds the advisory lock, or ctx ends. The
// lock is a session lock on its own connection, released by unlock or by
// the connection closing.
func (d DB) Lock(ctx context.Context) (func(), error) {
	conn, err := pgx.Connect(ctx, d.DSN)
	if err != nil {
		return nil, fmt.Errorf("connecting for the lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", LockKey); err != nil {
		conn.Close(context.Background())
		return nil, fmt.Errorf("taking the lock: %w", err)
	}
	return func() {
		conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", LockKey)
		conn.Close(context.Background())
	}, nil
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
