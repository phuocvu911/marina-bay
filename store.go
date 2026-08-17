package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the build stays static
)

// Persistence. The tracker in tracker.go stays the hot path for live zone
// decisions; SQLite is written on ingest and read once at startup, never on a
// request. Two tables with deliberately different lifetimes:
//
//   - sightings   raw history, one row per resolved sighting. Swept on a timer
//     (retention.go) — it is only useful for "where has this been
//     in the last hour", and it grows at roughly one row per beacon
//     per gateway per second.
//   - asset_state one row per asset, upserted on every sighting. Never swept.
//     This is the answer to "where did we last see the trolley",
//     which has to survive both a redeploy and an asset that has
//     been switched off in a shed since March.
const schema = `
CREATE TABLE IF NOT EXISTS sightings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    minor INTEGER NOT NULL,
    gateway TEXT NOT NULL,
    rssi INTEGER NOT NULL,
    seen_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sightings_seen_at ON sightings(seen_at);

CREATE TABLE IF NOT EXISTS asset_state (
    minor INTEGER PRIMARY KEY,
    zone TEXT NOT NULL,
    rssi INTEGER NOT NULL,
    last_seen INTEGER NOT NULL
);
`

// Store owns the database handle and the only SQL in the program.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the database at path and applies the
// schema. Missing parent directories are created so a fresh Fly volume — or a
// laptop with no ./data yet — works without a setup step.
func OpenStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	// WAL keeps the ingest writes from blocking the startup read and survives
	// an unclean machine stop; busy_timeout turns a momentary lock into a wait
	// rather than an error the gateway would never retry.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One writer. Ingest is a handful of rows a second against a file on a
	// single volume, so there is nothing to gain from concurrent connections
	// and plenty to lose to "database is locked".
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// DB exposes the handle for the retention sweeper, which owns its own SQL.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

// Record persists a batch of resolved sightings: every one appends to the raw
// history, and each also refreshes the asset's durable last-known state. One
// transaction per gateway batch rather than per beacon, so a gateway reporting
// a dozen assets costs one fsync instead of a dozen.
func (s *Store) Record(sightings ...Sighting) error {
	if len(sightings) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once Commit succeeds

	insert, err := tx.Prepare(`INSERT INTO sightings (minor, gateway, rssi, seen_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()

	// The guard on last_seen keeps a batch that arrived late — a gateway
	// catching up after a network drop — from dragging an asset's state back
	// to an older zone than one we have already recorded.
	upsert, err := tx.Prepare(`
		INSERT INTO asset_state (minor, zone, rssi, last_seen) VALUES (?, ?, ?, ?)
		ON CONFLICT(minor) DO UPDATE SET
			zone = excluded.zone,
			rssi = excluded.rssi,
			last_seen = excluded.last_seen
		WHERE excluded.last_seen >= asset_state.last_seen`)
	if err != nil {
		return err
	}
	defer upsert.Close()

	for _, g := range sightings {
		at := g.At.Unix()
		if _, err := insert.Exec(g.Minor, g.Gateway, g.RSSI, at); err != nil {
			return err
		}
		if _, err := upsert.Exec(g.Minor, g.Zone, g.ZoneRSSI, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadAssetState reads every asset's last known zone, for seeding the tracker
// at startup.
func (s *Store) LoadAssetState() ([]AssetState, error) {
	rows, err := s.db.Query(`SELECT minor, zone, rssi, last_seen FROM asset_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AssetState
	for rows.Next() {
		var (
			st       AssetState
			lastSeen int64
		)
		if err := rows.Scan(&st.Minor, &st.Zone, &st.RSSI, &lastSeen); err != nil {
			return nil, err
		}
		st.LastSeen = time.Unix(lastSeen, 0)
		out = append(out, st)
	}
	return out, rows.Err()
}
