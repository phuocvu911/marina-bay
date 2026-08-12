package main

import (
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Zone resolution: loudest-gateway-wins over smoothed RSSI, with hysteresis.
const (
	emaAlpha    = 0.3              // ema = 0.3*new + 0.7*old
	freshWindow = 5 * time.Second  // readings older than this don't compete
	staleAfter  = 30 * time.Second // asset unheard for this long = offline
	hysteresis  = 4.0              // dBm a challenger must win by to take over
)

// Proximity hint thresholds on the winning EMA.
const (
	veryCloseRSSI = -55.0
	nearbyRSSI    = -75.0
)

// reading is the smoothed signal for one (asset, gateway) pair.
type reading struct {
	ema      float64
	lastSeen time.Time
}

type assetTrack struct {
	minor    uint16
	readings map[string]*reading // gateway MAC -> smoothed reading
	zoneGW   string              // gateway currently owning the asset
	lastSeen time.Time           // most recent sighting on any gateway
}

// Tracker holds all in-memory state: per-asset signal readings and gateway
// heartbeats. Safe for concurrent use.
type Tracker struct {
	mu         sync.RWMutex
	tracks     map[uint16]*assetTrack
	heartbeats map[string]time.Time // gateway MAC -> last heartbeat
	now        func() time.Time     // injectable clock for tests
}

func NewTracker() *Tracker {
	return &Tracker{
		tracks:     make(map[uint16]*assetTrack),
		heartbeats: make(map[string]time.Time),
		now:        time.Now,
	}
}

// Observe records one sighting of an asset by a gateway and re-resolves the
// asset's zone.
func (t *Tracker) Observe(minor uint16, gatewayMAC string, rssi int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	a := t.tracks[minor]
	if a == nil {
		a = &assetTrack{minor: minor, readings: make(map[string]*reading)}
		t.tracks[minor] = a
	}

	r := a.readings[gatewayMAC]
	if r == nil || now.Sub(r.lastSeen) > freshWindow {
		// First reading, or the old EMA is too stale to blend with — the
		// asset may have physically moved since. Start over from the raw
		// value rather than dragging in minutes-old signal.
		a.readings[gatewayMAC] = &reading{ema: float64(rssi), lastSeen: now}
	} else {
		r.ema = emaAlpha*float64(rssi) + (1-emaAlpha)*r.ema
		r.lastSeen = now
	}
	a.lastSeen = now

	a.resolveZone(now)
}

// resolveZone picks the owning gateway. The current owner keeps the asset
// unless a fresh challenger beats it by >= hysteresis dBm; if the owner's
// reading has gone stale, the loudest fresh gateway takes over outright.
func (a *assetTrack) resolveZone(now time.Time) {
	cur := a.readings[a.zoneGW]
	curFresh := cur != nil && now.Sub(cur.lastSeen) <= freshWindow

	bestGW, bestEMA := "", math.Inf(-1)
	for gw, r := range a.readings {
		if gw == a.zoneGW || now.Sub(r.lastSeen) > freshWindow {
			continue
		}
		if r.ema > bestEMA {
			bestGW, bestEMA = gw, r.ema
		}
	}
	if bestGW == "" {
		return // no fresh challenger; keep current zone (staleness is handled at read time)
	}
	if !curFresh || bestEMA >= cur.ema+hysteresis {
		a.zoneGW = bestGW
	}
}

// Heartbeat records a gateway heartbeat.
func (t *Tracker) Heartbeat(gatewayMAC string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.heartbeats[gatewayMAC] = t.now()
}

// AssetView is the read-model served to the API and UI.
type AssetView struct {
	Name         string    `json:"name"`
	Minor        uint16    `json:"minor"`
	Zone         string    `json:"zone"`          // "" if never seen
	Proximity    string    `json:"proximity"`     // "" unless online
	RSSI         int       `json:"rssi"`          // winning EMA, rounded; 0 if never seen
	SecondsSince int       `json:"seconds_since"` // -1 if never seen
	LastSeen     time.Time `json:"last_seen"`     // zero if never seen
	Online       bool      `json:"online"`
}

// Assets returns the current view of every registered asset (even ones never
// heard from) plus any fleet beacons seen with an unregistered minor.
func (t *Tracker) Assets() []AssetView {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := t.now()

	views := make(map[uint16]AssetView, len(assets)+len(t.tracks))
	for minor, name := range assets {
		views[minor] = AssetView{Name: name, Minor: minor, SecondsSince: -1}
	}
	for minor, a := range t.tracks {
		name := assetName(minor)
		if name == "" {
			name = "unregistered beacon " + itoa(minor)
		}
		age := now.Sub(a.lastSeen)
		v := AssetView{
			Name:         name,
			Minor:        minor,
			SecondsSince: int(age.Seconds()),
			LastSeen:     a.lastSeen,
			Online:       age <= staleAfter,
		}
		// Every track that Observe creates has a reading for its owning
		// gateway, and readings are never deleted — but this is a read path
		// serving live requests, and one future cleanup that drops a stale
		// reading would turn a missing entry into a nil dereference here.
		// Report what we have instead.
		if r := a.readings[a.zoneGW]; r != nil {
			v.Zone = zoneName(a.zoneGW)
			v.RSSI = int(math.Round(r.ema))
			if v.Online {
				v.Proximity = proximityHint(r.ema)
			}
		}
		views[minor] = v
	}

	out := make([]AssetView, 0, len(views))
	for _, v := range views {
		out = append(out, v)
	}
	sortViews(out)
	return out
}

// Gateways returns each configured gateway's zone and last heartbeat.
type GatewayView struct {
	MAC              string    `json:"mac"`
	Zone             string    `json:"zone"`
	LastHeartbeat    time.Time `json:"last_heartbeat"` // zero if never
	SecondsSinceBeat int       `json:"seconds_since"`  // -1 if never
}

func (t *Tracker) Gateways() []GatewayView {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := t.now()

	// Declaration order is west to east, which is the useful order to read a
	// floor in — more useful than sorting by name.
	out := make([]GatewayView, 0, len(zones))
	for _, z := range zones {
		v := GatewayView{MAC: z.MAC, Zone: z.Name, SecondsSinceBeat: -1}
		if hb, ok := t.heartbeats[z.MAC]; ok {
			v.LastHeartbeat = hb
			v.SecondsSinceBeat = int(now.Sub(hb).Seconds())
		}
		out = append(out, v)
	}
	return out
}

func itoa(minor uint16) string {
	return strconv.Itoa(int(minor))
}

func sortViews(v []AssetView) {
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
}

func proximityHint(ema float64) string {
	switch {
	case ema >= veryCloseRSSI:
		return "very close"
	case ema >= nearbyRSSI:
		return "nearby"
	default:
		return "in the area"
	}
}
