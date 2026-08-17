package main

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a store on a fresh file under the test's temp directory
// and returns it with the path, so a test can close and reopen the same file.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marina.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	// Table names cannot be bound as parameters; every caller passes a literal.
	if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestStoreRecordWritesBothTables(t *testing.T) {
	store, _ := newTestStore(t)
	at := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	err := store.Record(
		Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: at},
		Sighting{Minor: 2, Gateway: gwB, RSSI: -70, Zone: "Stair A", ZoneRSSI: -70, At: at},
	)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	if n := countRows(t, store, "sightings"); n != 2 {
		t.Errorf("sightings rows = %d, want 2", n)
	}
	states, err := store.LoadAssetState()
	if err != nil {
		t.Fatalf("load asset state: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("asset_state rows = %d, want 2", len(states))
	}
}

// Raw history accumulates a row per sighting; last-known state must not — it
// is one row per asset, overwritten in place.
func TestStoreAppendsHistoryButUpsertsState(t *testing.T) {
	store, _ := newTestStore(t)
	at := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		s := Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: at.Add(time.Duration(i) * time.Second)}
		if err := store.Record(s); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	if n := countRows(t, store, "sightings"); n != 3 {
		t.Errorf("sightings rows = %d, want 3 (history appends)", n)
	}
	if n := countRows(t, store, "asset_state"); n != 1 {
		t.Errorf("asset_state rows = %d, want 1 (state upserts)", n)
	}
}

func TestStoreUpsertMovesAssetToNewZone(t *testing.T) {
	store, _ := newTestStore(t)
	at := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	mustRecord(t, store, Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: at})
	mustRecord(t, store, Sighting{Minor: 1, Gateway: gwB, RSSI: -50, Zone: "Stair A", ZoneRSSI: -52, At: at.Add(time.Minute)})

	states := mustLoad(t, store)
	if len(states) != 1 {
		t.Fatalf("got %d states, want 1", len(states))
	}
	got := states[0]
	if got.Zone != "Stair A" || got.RSSI != -52 || !got.LastSeen.Equal(at.Add(time.Minute)) {
		t.Errorf("state = %+v, want Stair A / -52 / %v", got, at.Add(time.Minute))
	}
}

// A gateway catching up after a network drop replays older readings. Those must
// not drag an asset's last-known state backwards to a zone it has since left.
func TestStoreIgnoresOutOfOrderState(t *testing.T) {
	store, _ := newTestStore(t)
	at := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	mustRecord(t, store, Sighting{Minor: 1, Gateway: gwB, RSSI: -50, Zone: "Stair A", ZoneRSSI: -50, At: at})
	mustRecord(t, store, Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: at.Add(-time.Minute)})

	states := mustLoad(t, store)
	if states[0].Zone != "Stair A" || !states[0].LastSeen.Equal(at) {
		t.Errorf("state = %+v, want the newer Stair A reading to stand", states[0])
	}
	// The late sighting still belongs in the raw history, just not in state.
	if n := countRows(t, store, "sightings"); n != 2 {
		t.Errorf("sightings rows = %d, want 2 (history keeps late readings)", n)
	}
}

// The whole point of asset_state: the answer to "where was it last seen"
// outlives the process.
func TestStoreReopenPreservesAssetState(t *testing.T) {
	store, path := newTestStore(t)
	at := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	mustRecord(t, store, Sighting{Minor: 3, Gateway: gwA, RSSI: -66, Zone: "West Wing", ZoneRSSI: -66, At: at})
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	states := mustLoad(t, reopened)
	if len(states) != 1 {
		t.Fatalf("got %d states after reopen, want 1", len(states))
	}
	if states[0].Minor != 3 || states[0].Zone != "West Wing" || states[0].RSSI != -66 {
		t.Errorf("restored state = %+v, want minor 3 in West Wing at -66", states[0])
	}
	if !states[0].LastSeen.Equal(at) {
		t.Errorf("restored last_seen = %v, want %v", states[0].LastSeen, at)
	}
}

// OpenStore is run on every boot against a volume that already has a database
// on it, so applying the schema a second time has to be a no-op.
func TestOpenStoreIsIdempotent(t *testing.T) {
	store, path := newTestStore(t)
	mustRecord(t, store, Sighting{Minor: 1, Gateway: gwA, RSSI: -60, Zone: "West Wing", ZoneRSSI: -60, At: time.Now()})
	store.Close()

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if n := countRows(t, reopened, "sightings"); n != 1 {
		t.Errorf("sightings rows after reopen = %d, want 1 (schema reapply must not wipe data)", n)
	}
}

// A fresh Fly volume is an empty mount point; the database's directory may not
// exist yet on the first boot.
func TestOpenStoreCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "nested", "marina.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store in a missing directory: %v", err)
	}
	store.Close()
}

func TestRecordWithNoSightingsIsNoOp(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Record(); err != nil {
		t.Fatalf("record with no sightings: %v", err)
	}
	if n := countRows(t, store, "sightings"); n != 0 {
		t.Errorf("sightings rows = %d, want 0", n)
	}
}

// Restore is what turns persisted state back into a live tracker answer.
func TestRestoreSeedsTracker(t *testing.T) {
	tr, now := newTestTracker()
	seen := now.Add(-10 * time.Second)
	tr.Restore([]AssetState{{Minor: 1, Zone: "West Wing", RSSI: -62, LastSeen: seen}})

	v := findAsset(t, tr, 1)
	if v.Zone != "West Wing" || v.RSSI != -62 {
		t.Errorf("restored view = %+v, want West Wing at -62", v)
	}
	if !v.Online || v.SecondsSince != 10 {
		t.Errorf("restored view = %+v, want online with seconds_since 10", v)
	}
}

// Restored state that is already past the staleness cutoff — the normal case
// after a redeploy — shows offline but keeps the zone, same as any other
// asset that has gone quiet.
func TestRestoreStaleStateShowsOfflineWithZone(t *testing.T) {
	tr, now := newTestTracker()
	tr.Restore([]AssetState{{Minor: 1, Zone: "West Wing", RSSI: -62, LastSeen: now.Add(-2 * time.Hour)}})

	v := findAsset(t, tr, 1)
	if v.Online {
		t.Error("asset restored from two-hour-old state should be offline")
	}
	if v.Zone != "West Wing" {
		t.Errorf("zone = %q, want West Wing (last-known position is never dropped)", v.Zone)
	}
}

// A restored asset must go on competing normally. Its seeded reading is stale
// by the time anything new arrives, so the first gateway to report takes it
// without needing to clear the hysteresis margin.
func TestRestoredAssetResolvesNormallyAfterwards(t *testing.T) {
	tr, now := newTestTracker()
	tr.Restore([]AssetState{{Minor: 1, Zone: "West Wing", RSSI: -50, LastSeen: now.Add(-time.Hour)}})

	tr.Observe(1, gwB, -80) // far quieter, but the only fresh reading
	if got := findAsset(t, tr, 1).Zone; got != "Stair A" {
		t.Errorf("zone = %q, want Stair A (a stale restored reading loses outright)", got)
	}
}

// If a gateway is retired from the registry between deploys, its zone name no
// longer maps to a MAC. The last-known zone is still the most useful thing we
// can say, so it must survive as a label rather than being dropped.
func TestRestoreKeepsZoneOfUnknownGateway(t *testing.T) {
	tr, now := newTestTracker()
	tr.Restore([]AssetState{{Minor: 1, Zone: "Old Boathouse", RSSI: -62, LastSeen: now.Add(-time.Minute)}})

	v := findAsset(t, tr, 1)
	if v.Zone != "Old Boathouse" {
		t.Errorf("zone = %q, want the persisted Old Boathouse label", v.Zone)
	}
	if v.RSSI != 0 || v.Proximity != "" {
		t.Errorf("view = %+v, want no signal data for a gateway that is gone", v)
	}
}

func mustRecord(t *testing.T, s *Store, sightings ...Sighting) {
	t.Helper()
	if err := s.Record(sightings...); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func mustLoad(t *testing.T, s *Store) []AssetState {
	t.Helper()
	states, err := s.LoadAssetState()
	if err != nil {
		t.Fatalf("load asset state: %v", err)
	}
	return states
}
