package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Configuration ──────────────────────────────────────────────
const (
	// Only track beacons from OUR fleet. Right now this is the i3 factory
	// UUID; ideally give your beacons a custom UUID in BeaconSET+ so ambient
	// third-party iBeacons in the marina can't pollute your data.
	fleetUUID = "E2C56DB5-DFFB-48D2-B060-D0F5A71096E0"

	// An asset not heard from within this window is considered "stale".
	staleAfter = 30 * time.Second
)

// registry maps a beacon's minor -> human-readable asset name.
// The NAME lives here in software; the beacon only carries the number.
var registry = map[uint16]string{
	1: "Hari",
	2: "Juho",
}

func assetName(minor uint16) string {
	if name, ok := registry[minor]; ok {
		return name
	}
	return fmt.Sprintf("unknown (minor %d)", minor)
}

// ── Wire format ────────────────────────────────────────────────
// The G1-E POSTs a JSON array. Entries are heterogeneous:
//   - gateway heartbeat: {"gateway","timestamp","seq"}
//   - beacon sighting:   {"mac","timestamp","rssi","raw"}
type rawEntry struct {
	Gateway   string `json:"gateway"`
	Seq       int    `json:"seq"`
	MAC       string `json:"mac"`
	RSSI      int    `json:"rssi"`
	Raw       string `json:"raw"` // base64 BLE advertising payload
	Timestamp int64  `json:"timestamp"`
}

// ── Parsed identity ────────────────────────────────────────────
type iBeacon struct {
	UUID    string
	Major   uint16
	Minor   uint16
	TxPower int8 // measured RSSI at 1m, used later for distance estimates
}

// parseIBeacon walks the BLE advertising structures ([len][type][payload]…)
// and extracts the iBeacon frame if present.
func parseIBeacon(adv []byte) (iBeacon, bool) {
	for i := 0; i < len(adv); {
		length := int(adv[i])
		if length == 0 || i+1+length > len(adv) {
			break
		}
		adType := adv[i+1]
		payload := adv[i+2 : i+1+length]

		// 0xFF = manufacturer-specific. iBeacon = Apple (0x004C) + type 0x02 len 0x15.
		if adType == 0xFF && len(payload) >= 25 &&
			payload[0] == 0x4C && payload[1] == 0x00 &&
			payload[2] == 0x02 && payload[3] == 0x15 {
			return iBeacon{
				UUID:    formatUUID(payload[4:20]),
				Major:   binary.BigEndian.Uint16(payload[20:22]),
				Minor:   binary.BigEndian.Uint16(payload[22:24]),
				TxPower: int8(payload[24]),
			}, true
		}
		i += 1 + length
	}
	return iBeacon{}, false
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ── Last-seen state ────────────────────────────────────────────
// Where is each asset right now? We keep the most recent sighting per minor.
type assetState struct {
	Name        string
	Minor       uint16
	LastRSSI    int
	LastGateway string
	LastSeen    time.Time
}

type store struct {
	mu     sync.RWMutex
	assets map[uint16]*assetState
}

func newStore() *store {
	return &store{assets: make(map[uint16]*assetState)}
}

func (s *store) update(b iBeacon, rssi int, gateway string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[b.Minor] = &assetState{
		Name:        assetName(b.Minor),
		Minor:       b.Minor,
		LastRSSI:    rssi,
		LastGateway: gateway,
		LastSeen:    time.Now(),
	}
}

// snapshot returns a copy of current state, sorted by name, for safe reading.
func (s *store) snapshot() []assetState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]assetState, 0, len(s.assets))
	for _, a := range s.assets {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── HTTP handlers ──────────────────────────────────────────────
func (s *store) ingestHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var entries []rawEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		log.Printf("unmarshal failed: %v\nbody: %s", err, body)
		w.WriteHeader(http.StatusOK) // still 200 so the gateway keeps sending
		return
	}

	// First pass: find which gateway sent this batch (its heartbeat entry).
	gateway := "unknown"
	for _, e := range entries {
		if e.Gateway != "" {
			gateway = e.Gateway
		}
	}

	// Second pass: record each fleet beacon sighting.
	for _, e := range entries {
		if e.MAC == "" || e.Raw == "" {
			continue
		}
		adv, err := base64.StdEncoding.DecodeString(e.Raw)
		if err != nil {
			continue
		}
		b, ok := parseIBeacon(adv)
		if !ok {
			continue // Eddystone or other frame
		}
		if !strings.EqualFold(b.UUID, fleetUUID) {
			continue // not our beacon, ignore ambient iBeacons
		}
		s.update(b, e.RSSI, gateway)
		log.Printf("[SIGHTING] %-18s rssi=%d dBm  via %s", assetName(b.Minor), e.RSSI, gateway)
	}

	w.WriteHeader(http.StatusOK)
}

// assetsHandler answers "where is everything right now?" as JSON.
func (s *store) assetsHandler(w http.ResponseWriter, r *http.Request) {
	type view struct {
		Name       string `json:"name"`
		Minor      uint16 `json:"minor"`
		RSSI       int    `json:"rssi"`
		Gateway    string `json:"gateway"`
		SecondsAgo int    `json:"seconds_ago"`
		Online     bool   `json:"online"`
	}
	var out []view
	for _, a := range s.snapshot() {
		age := time.Since(a.LastSeen)
		out = append(out, view{
			Name:       a.Name,
			Minor:      a.Minor,
			RSSI:       a.LastRSSI,
			Gateway:    a.LastGateway,
			SecondsAgo: int(age.Seconds()),
			Online:     age < staleAfter,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func main() {
	st := newStore()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest", st.ingestHandler)
	mux.HandleFunc("GET /assets", st.assetsHandler)

	addr := ":8080"
	log.Printf("marina ingest listening on %s  (POST /ingest, GET /assets)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
