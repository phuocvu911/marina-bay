package main

import (
	"testing"
	"time"
)

// The sweep is the only thing that deletes anything in this program, so what
// it leaves behind matters as much as what it removes.
func TestSweepDeletesOldSightingsAndKeepsRecentOnes(t *testing.T) {
	store, _ := newTestStore(t)
	now := time.Now()

	mustRecord(t, store,
		Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: now.Add(-3 * time.Hour)},
		Sighting{Minor: 1, Gateway: gwA, RSSI: -61, Zone: "West Wing", ZoneRSSI: -61, At: now.Add(-90 * time.Minute)},
		Sighting{Minor: 1, Gateway: gwA, RSSI: -62, Zone: "West Wing", ZoneRSSI: -62, At: now.Add(-30 * time.Minute)},
		Sighting{Minor: 2, Gateway: gwB, RSSI: -70, Zone: "Stair A", ZoneRSSI: -70, At: now},
	)

	n, err := sweepSightings(store.DB(), time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d rows, want 2 (the 3h and 90m sightings)", n)
	}
	if got := countRows(t, store, "sightings"); got != 2 {
		t.Errorf("%d sightings left, want 2 (the 30m and current ones)", got)
	}

	// Confirm it kept the right two rather than merely the right number.
	var oldest int64
	if err := store.DB().QueryRow(`SELECT min(seen_at) FROM sightings`).Scan(&oldest); err != nil {
		t.Fatalf("query oldest: %v", err)
	}
	if cutoff := now.Add(-time.Hour).Unix(); oldest < cutoff {
		t.Errorf("oldest surviving row is %d, older than the cutoff %d", oldest, cutoff)
	}
}

// asset_state is the durable record. Sweeping history must never take an
// asset's last-known position with it, however old that position is.
func TestSweepLeavesAssetStateAlone(t *testing.T) {
	store, _ := newTestStore(t)
	old := time.Now().Add(-30 * 24 * time.Hour) // a month in a shed

	mustRecord(t, store, Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: old})

	if _, err := sweepSightings(store.DB(), time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := countRows(t, store, "sightings"); got != 0 {
		t.Errorf("%d sightings left, want 0", got)
	}

	states := mustLoad(t, store)
	if len(states) != 1 {
		t.Fatalf("asset_state rows = %d, want 1 — the sweep must not touch it", len(states))
	}
	if states[0].Zone != "West Wing" || !states[0].LastSeen.Equal(old.Truncate(time.Second)) {
		t.Errorf("state = %+v, want the month-old West Wing position intact", states[0])
	}
}

func TestSweepOnEmptyTableIsHarmless(t *testing.T) {
	store, _ := newTestStore(t)
	n, err := sweepSightings(store.DB(), time.Hour)
	if err != nil {
		t.Fatalf("sweep on an empty table: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted %d rows from an empty table, want 0", n)
	}
}

// A retention window wide enough to cover everything should delete nothing —
// the guard against an off-by-one that would wipe the table on every tick.
func TestSweepKeepsEverythingInsideTheWindow(t *testing.T) {
	store, _ := newTestStore(t)
	now := time.Now()
	mustRecord(t, store,
		Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: now.Add(-time.Hour)},
		Sighting{Minor: 1, Gateway: gwA, RSSI: -61, Zone: "West Wing", ZoneRSSI: -61, At: now},
	)

	n, err := sweepSightings(store.DB(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted %d rows inside the retention window, want 0", n)
	}
	if got := countRows(t, store, "sightings"); got != 2 {
		t.Errorf("%d sightings left, want 2", got)
	}
}

// startRetentionSweeper hands the goroutine a live handle; a sweeper that
// panicked on a nil or closed database would take the process down with it.
func TestStartRetentionSweeperDoesNotBlock(t *testing.T) {
	store, _ := newTestStore(t)
	startRetentionSweeper(store.DB(), time.Hour) // first tick is 10 minutes out
	if got := countRows(t, store, "sightings"); got != 0 {
		t.Errorf("sightings = %d, want 0", got)
	}
}
