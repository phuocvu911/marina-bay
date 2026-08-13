package main

import (
	"database/sql"
	"log"
	"time"
)

// sweepInterval is how often old raw sightings are cleared out. Well under the
// default retention window, so rows are never more than a sweep past their
// deadline, and rare enough that the delete never competes with ingest.
const sweepInterval = 10 * time.Minute

// startRetentionSweeper runs the sweep on a ticker for the life of the process.
// It is fire-and-forget on purpose: the tracker answers from memory, so a
// sweeper that dies takes nothing down with it — the database just grows until
// the next restart, which the per-sweep log makes visible.
func startRetentionSweeper(db *sql.DB, maxAge time.Duration) {
	ticker := time.NewTicker(sweepInterval)
	go func() {
		for range ticker.C {
			n, err := sweepSightings(db, maxAge)
			if err != nil {
				log.Printf("retention sweep failed: %v", err)
				continue
			}
			log.Printf("retention sweep: deleted %d rows older than %s", n, maxAge)
		}
	}()
}

// sweepSightings deletes raw sightings older than maxAge and reports how many
// went. asset_state is deliberately untouched: it is one small row per asset
// and the last-known position has to outlive the history it was derived from.
func sweepSightings(db *sql.DB, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	res, err := db.Exec(`DELETE FROM sightings WHERE seen_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
