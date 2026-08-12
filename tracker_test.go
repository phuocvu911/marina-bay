package main

import (
	"testing"
	"time"
)

const (
	gwA = "ac233fc26fb0" // West Wing
	gwB = "ac233fc270d4" // Stair A
)

// newTestTracker returns a tracker with a controllable clock.
func newTestTracker() (*Tracker, *time.Time) {
	tr := NewTracker()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	tr.now = func() time.Time { return now }
	return tr, &now
}

func findAsset(t *testing.T, tr *Tracker, minor uint16) AssetView {
	t.Helper()
	for _, v := range tr.Assets() {
		if v.Minor == minor {
			return v
		}
	}
	t.Fatalf("asset minor=%d not in Assets()", minor)
	return AssetView{}
}

func TestEMASmoothing(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe(1, gwA, -60)
	tr.Observe(1, gwA, -50)
	// ema = 0.3*(-50) + 0.7*(-60) = -57
	if got := findAsset(t, tr, 1).RSSI; got != -57 {
		t.Errorf("smoothed RSSI = %d, want -57", got)
	}
}

func TestEMAResetsAfterStaleGap(t *testing.T) {
	tr, now := newTestTracker()
	tr.Observe(1, gwA, -60)
	*now = now.Add(6 * time.Second) // beyond the 5s freshness window
	tr.Observe(1, gwA, -40)
	// Old EMA is too stale to blend with; start over from the raw value.
	if got := findAsset(t, tr, 1).RSSI; got != -40 {
		t.Errorf("RSSI after stale gap = %d, want -40 (reset, not blended)", got)
	}
}

func TestHysteresisNoFlipUnderMargin(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe(1, gwA, -60)
	tr.Observe(1, gwB, -57) // only 3 dBm louder — must not steal the asset
	if got := findAsset(t, tr, 1).Zone; got != "West Wing" {
		t.Errorf("zone = %q, want West Wing (3 dBm is under the 4 dBm margin)", got)
	}
}

func TestHysteresisFlipsAtMargin(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe(1, gwA, -60)
	tr.Observe(1, gwB, -56) // exactly 4 dBm louder — takes over
	if got := findAsset(t, tr, 1).Zone; got != "Stair A" {
		t.Errorf("zone = %q, want Stair A (4 dBm meets the margin)", got)
	}
}

func TestHysteresisFlipsAsChallengerEMAClimbs(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe(1, gwA, -60)
	tr.Observe(1, gwB, -57) // ema -57: under margin, stays
	if got := findAsset(t, tr, 1).Zone; got != "West Wing" {
		t.Fatalf("zone = %q, want West Wing before margin met", got)
	}
	tr.Observe(1, gwB, -50) // ema = 0.3*(-50)+0.7*(-57) = -54.9: beats -56
	if got := findAsset(t, tr, 1).Zone; got != "Stair A" {
		t.Errorf("zone = %q, want Stair A once EMA clears the margin", got)
	}
}

func TestStaleCurrentZoneSwitchesWithoutMargin(t *testing.T) {
	tr, now := newTestTracker()
	tr.Observe(1, gwA, -60)
	*now = now.Add(6 * time.Second) // gwA's reading falls out of the freshness window
	tr.Observe(1, gwB, -80)         // much quieter, but the only fresh gateway
	if got := findAsset(t, tr, 1).Zone; got != "Stair A" {
		t.Errorf("zone = %q, want Stair A (stale owner loses without margin)", got)
	}
}

func TestStalenessTransition(t *testing.T) {
	tr, now := newTestTracker()
	tr.Observe(1, gwA, -60)

	*now = now.Add(29 * time.Second)
	if v := findAsset(t, tr, 1); !v.Online {
		t.Error("asset should still be online at 29s")
	}

	*now = now.Add(2 * time.Second) // 31s total
	v := findAsset(t, tr, 1)
	if v.Online {
		t.Error("asset should be offline after 30s of silence")
	}
	if v.Zone != "West Wing" {
		t.Errorf("last-seen zone = %q, want West Wing (never delete last-seen info)", v.Zone)
	}
	if v.SecondsSince != 31 {
		t.Errorf("seconds_since = %d, want 31", v.SecondsSince)
	}
}

func TestNeverSeenAssetListedOffline(t *testing.T) {
	tr, _ := newTestTracker()
	v := findAsset(t, tr, 2) // trolley, registered but never heard
	if v.Name != "trolley" || v.Online || v.SecondsSince != -1 || v.Zone != "" {
		t.Errorf("never-seen asset view = %+v, want offline trolley with no zone", v)
	}
}

// Assets() is a live request path. A track whose owning gateway has no reading
// is unreachable through Observe today, but it must degrade to "no signal
// data" rather than panicking if that ever changes.
func TestAssetsSurvivesOwningGatewayWithNoReading(t *testing.T) {
	tr, _ := newTestTracker()
	tr.tracks[7] = &assetTrack{
		minor:    7,
		readings: map[string]*reading{},
		zoneGW:   gwA,
		lastSeen: tr.now(),
	}

	v := findAsset(t, tr, 7) // must not panic
	if v.RSSI != 0 || v.Proximity != "" || v.Zone != "" {
		t.Errorf("view = %+v, want no zone or signal data when the owner has no reading", v)
	}
}

func TestProximityHint(t *testing.T) {
	cases := []struct {
		ema  float64
		want string
	}{
		{-40, "very close"},
		{-55, "very close"},
		{-56, "nearby"},
		{-75, "nearby"},
		{-76, "in the area"},
	}
	for _, c := range cases {
		if got := proximityHint(c.ema); got != c.want {
			t.Errorf("proximityHint(%v) = %q, want %q", c.ema, got, c.want)
		}
	}
}
