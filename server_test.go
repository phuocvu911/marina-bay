package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The exact sample batch from the G1-E capture: one heartbeat, one sighting
// whose raw payload decodes to fleet UUID, minor 0.
const sampleBatch = `[
  {"gateway":"ac233fc26fb0","timestamp":1782556716937,"seq":146},
  {"mac":"c3000076542a","timestamp":1782556717035,"rssi":-35,
   "raw":"AgEGGv9MAAIV4sVttd/7SNKwYND1pxCW4AAAAADF"}
]`

func newTestServer() *httptest.Server {
	return httptest.NewServer(NewServer(NewTracker(), nil, false).Routes())
}

// newPersistentTestServer wires a server to a store on disk, so a test can
// close it and reopen the same file to model a restart.
func newPersistentTestServer(t *testing.T, dbPath string) (*httptest.Server, *Store) {
	t.Helper()
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	tracker := NewTracker()
	states, err := store.LoadAssetState()
	if err != nil {
		t.Fatalf("load asset state: %v", err)
	}
	tracker.Restore(states)
	return httptest.NewServer(NewServer(tracker, store, false).Routes()), store
}

func getAssets(t *testing.T, ts *httptest.Server, query string) []AssetView {
	t.Helper()
	res, err := http.Get(ts.URL + "/api/assets" + query)
	if err != nil {
		t.Fatalf("GET /api/assets: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/assets status = %d", res.StatusCode)
	}
	var views []AssetView
	if err := json.NewDecoder(res.Body).Decode(&views); err != nil {
		t.Fatalf("decode assets: %v", err)
	}
	return views
}

func findView(views []AssetView, minor uint16) (AssetView, bool) {
	for _, v := range views {
		if v.Minor == minor {
			return v, true
		}
	}
	return AssetView{}, false
}

func TestIngestSampleBatch(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	res, err := http.Post(ts.URL+"/ingest", "application/json", strings.NewReader(sampleBatch))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /ingest status = %d, want 200", res.StatusCode)
	}

	// The sighting (minor 0, unregistered) must show up in West Wing.
	for _, v := range getAssets(t, ts, "") {
		if v.Minor == 0 {
			if v.Zone != "West Wing" || !v.Online {
				t.Errorf("minor 0 view = %+v, want online in West Wing", v)
			}
			return
		}
	}
	t.Error("ingested beacon (minor 0) missing from /api/assets")
}

func TestIngestGarbageStillReturns200(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	res, err := http.Post(ts.URL+"/ingest", "application/json", strings.NewReader("not json at all"))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("POST /ingest with garbage status = %d, want 200 so the gateway keeps sending", res.StatusCode)
	}
}

// An oversized body must not be buffered whole, and must not take the tracker
// down with it — the next gateway to report has to still get through.
func TestIngestCapsBodySize(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	huge := strings.NewReader(strings.Repeat("x", maxIngestBytes*2))
	if res, err := http.Post(ts.URL+"/ingest", "application/json", huge); err == nil {
		res.Body.Close()
	}

	res, err := http.Post(ts.URL+"/ingest", "application/json", strings.NewReader(sampleBatch))
	if err != nil {
		t.Fatalf("POST /ingest after an oversized body: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status after an oversized body = %d, want 200", res.StatusCode)
	}
	for _, v := range getAssets(t, ts, "") {
		if v.Minor == 0 {
			return
		}
	}
	t.Error("gateway could not ingest after an oversized body")
}

// Timeouts are the only thing standing between an open port and a connection
// that is held forever, so an unset one is a real regression.
func TestHTTPServerSetsTimeouts(t *testing.T) {
	srv := NewServer(NewTracker(), nil, false).HTTPServer(":0")
	for _, c := range []struct {
		name string
		got  time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout},
		{"WriteTimeout", srv.WriteTimeout},
		{"IdleTimeout", srv.IdleTimeout},
	} {
		if c.got <= 0 {
			t.Errorf("%s = %v, want a positive timeout", c.name, c.got)
		}
	}
	if srv.Handler == nil {
		t.Error("HTTPServer returned no handler")
	}
}

func TestAssetsQueryFilter(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	views := getAssets(t, ts, "?q=crad")
	if len(views) != 1 || views[0].Name != "cradle" {
		t.Fatalf("q=crad returned %+v, want just cradle", views)
	}

	if views := getAssets(t, ts, "?q=CRAD"); len(views) != 1 {
		t.Errorf("filter should be case-insensitive, q=CRAD returned %+v", views)
	}

	if views := getAssets(t, ts, "?q=nosuchasset"); len(views) != 0 {
		t.Errorf("q=nosuchasset returned %+v, want empty", views)
	}
}

func TestGatewaysHeartbeat(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	res, err := http.Post(ts.URL+"/ingest", "application/json", strings.NewReader(sampleBatch))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	res.Body.Close()

	res, err = http.Get(ts.URL + "/api/gateways")
	if err != nil {
		t.Fatalf("GET /api/gateways: %v", err)
	}
	defer res.Body.Close()
	var gws []GatewayView
	if err := json.NewDecoder(res.Body).Decode(&gws); err != nil {
		t.Fatalf("decode gateways: %v", err)
	}
	if len(gws) != len(zones) {
		t.Fatalf("got %d gateways, want %d", len(gws), len(zones))
	}
	for _, g := range gws {
		beat := g.SecondsSinceBeat >= 0
		if g.MAC == "ac233fc26fb0" && !beat {
			t.Error("gateway ac233fc26fb0 sent a heartbeat but shows none")
		}
		if g.MAC != "ac233fc26fb0" && beat {
			t.Errorf("gateway %s shows a heartbeat it never sent", g.MAC)
		}
	}
}

// A G1-E label prints its MAC uppercase and colon-separated. Typing it into
// the registry that way must still match the lowercase, separator-free MAC
// the gateway actually sends, or the sector silently never resolves.
func TestGatewayMACLabelFormatsMatch(t *testing.T) {
	want := zones[0].MAC
	for _, form := range []string{
		"AC233FC26FB0", "ac:23:3f:c2:6f:b0", "AC-23-3F-C2-6F-B0", "Ac233fC26Fb0",
	} {
		if got := normalizeMAC(form); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", form, got, want)
		}
		if zoneName(normalizeMAC(form)) != zones[0].Name {
			t.Errorf("%q did not resolve to %q", form, zones[0].Name)
		}
	}
}

// The whole point of asset_state: after a redeploy the UI answers "last seen
// in West Wing" straight away, before any gateway has reported again.
func TestRestartRestoresLastKnownZone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "marina.db")

	ts, store := newPersistentTestServer(t, dbPath)
	res, err := http.Post(ts.URL+"/ingest", "application/json", strings.NewReader(sampleBatch))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	res.Body.Close()

	before, ok := findView(getAssets(t, ts, ""), 0)
	if !ok || before.Zone != "West Wing" {
		t.Fatalf("before restart: minor 0 view = %+v, want West Wing", before)
	}
	ts.Close()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Restart: a brand new tracker and server over the same database file.
	restarted, _ := newPersistentTestServer(t, dbPath)
	defer restarted.Close()

	after, ok := findView(getAssets(t, restarted, ""), 0)
	if !ok {
		t.Fatal("minor 0 missing from /api/assets after restart")
	}
	if after.Zone != "West Wing" {
		t.Errorf("zone after restart = %q, want West Wing", after.Zone)
	}
	if after.LastSeen.Unix() != before.LastSeen.Unix() {
		t.Errorf("last_seen after restart = %v, want the persisted %v", after.LastSeen, before.LastSeen)
	}
}

// Persistence must not be able to break ingest: the tracker is the live path,
// and a closed database is logged, not returned to the gateway.
func TestIngestSurvivesADeadStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "marina.db")
	ts, store := newPersistentTestServer(t, dbPath)
	defer ts.Close()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	res, err := http.Post(ts.URL+"/ingest", "application/json", strings.NewReader(sampleBatch))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status with a dead store = %d, want 200 so the gateway keeps sending", res.StatusCode)
	}
	if v, ok := findView(getAssets(t, ts, ""), 0); !ok || v.Zone != "West Wing" {
		t.Errorf("minor 0 view = %+v, want the tracker to resolve it regardless", v)
	}
}

func TestIndexServesUI(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", res.StatusCode)
	}
	// Read it all: one Read is not guaranteed to return the whole body, and
	// the page outgrew a fixed buffer once the floor plan went inline.
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read index body: %v", err)
	}
	// Derived from the registry, not hardcoded: a literal list here is what
	// silently rotted last time the zone names changed.
	for _, z := range zones {
		if !strings.Contains(string(body), z.Name) {
			t.Errorf("index page missing zone %q", z.Name)
		}
	}
	if !strings.Contains(string(body), "app.js") {
		t.Error("index page does not load app.js")
	}
}

// The sector overlay is positioned from the normalised geometry in zones, so
// a zone whose rectangle never made it into the page would silently show the
// asset in the wrong place on the map.
func TestIndexPositionsEverySector(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read index body: %v", err)
	}
	for _, z := range zones {
		want := fmt.Sprintf("left:%.3f%%;top:%.3f%%;width:%.3f%%;height:%.3f%%",
			z.X*100, z.Y*100, z.W*100, z.H*100)
		if !strings.Contains(string(body), want) {
			t.Errorf("sector %q missing its overlay geometry (%s)", z.Name, want)
		}
		if !strings.Contains(string(body), `data-gw="`+z.MAC+`"`) {
			t.Errorf("gateway marker for %q (%s) not on the map", z.Name, z.MAC)
		}
	}
}
