package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The exact sample batch from the G1-E capture: one heartbeat, one sighting
// whose raw payload decodes to fleet UUID, minor 0.
const sampleBatch = `[
  {"gateway":"ac233fc26fb0","timestamp":1782556716937,"seq":146},
  {"mac":"c3000076542a","timestamp":1782556717035,"rssi":-35,
   "raw":"AgEGGv9MAAIV4sVttd/7SNKwYND1pxCW4AAAAADF"}
]`

func newTestServer() *httptest.Server {
	return httptest.NewServer(NewServer(NewTracker(), false).Routes())
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

	// The sighting (minor 0, unregistered) must show up in Race Office.
	for _, v := range getAssets(t, ts, "") {
		if v.Minor == 0 {
			if v.Zone != "Race Office" || !v.Online {
				t.Errorf("minor 0 view = %+v, want online in Race Office", v)
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
	if len(gws) != len(gateways) {
		t.Fatalf("got %d gateways, want %d", len(gws), len(gateways))
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
	buf := make([]byte, 4096)
	n, _ := res.Body.Read(buf)
	body := string(buf[:n])
	for _, want := range []string{"Race Office", "Club House", "Gas Station", "North Yard", "app.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}
