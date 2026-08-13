package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed templates static
var uiFS embed.FS

// Server carries handler dependencies; no globals beyond the registry maps.
// store may be nil — the tracker is the live path and the server answers
// normally without persistence.
type Server struct {
	tracker *Tracker
	store   *Store
	tmpl    *template.Template
	verbose bool
}

// tmplFuncs turn normalised zone geometry into CSS percentages, so the sector
// overlay lines up with the floor plan at whatever size it renders. Doing it
// server-side keeps the map meaningful with JavaScript switched off.
var tmplFuncs = template.FuncMap{
	"sectorStyle": func(z Zone) template.CSS {
		return template.CSS(fmt.Sprintf("left:%.3f%%;top:%.3f%%;width:%.3f%%;height:%.3f%%",
			z.X*100, z.Y*100, z.W*100, z.H*100))
	},
	"gatewayStyle": func(z Zone) template.CSS {
		return template.CSS(fmt.Sprintf("left:%.3f%%;top:%.3f%%", z.GwX*100, z.GwY*100))
	},
}

func NewServer(tracker *Tracker, store *Store, verbose bool) *Server {
	return &Server{
		tracker: tracker,
		store:   store,
		tmpl:    template.Must(template.New("ui").Funcs(tmplFuncs).ParseFS(uiFS, "templates/*.html")),
		verbose: verbose,
	}
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest", s.handleIngest)
	mux.HandleFunc("GET /api/assets", s.handleAssets)
	mux.HandleFunc("GET /api/gateways", s.handleGateways)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /static/", http.FileServer(http.FS(uiFS)))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	return mux
}

// HTTPServer wraps the routes in a server with timeouts. http.ListenAndServe
// applies none by default, so a client that opens a connection and then stalls
// mid-request holds it open forever; enough of those and the tracker stops
// answering while every gateway is still happily reporting. The read window is
// generous compared with a G1-E batch, and the idle window keeps the UI's 2 s
// polling on one connection rather than reconnecting every time.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// maxIngestBytes caps a single POST /ingest body. A gateway batch is a
// heartbeat plus one entry per beacon heard in the last second — tens of
// kilobytes at the very worst — so this is orders of magnitude of headroom
// while still keeping an unauthenticated endpoint from being handed an
// unbounded body to buffer in memory.
const maxIngestBytes = 1 << 20 // 1 MiB

// handleIngest accepts a G1-E payload. It always answers 200 — the gateway
// must keep sending even if we couldn't make sense of one batch — so parse
// errors are logged, never returned.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIngestBytes))
	if err != nil {
		log.Printf("ingest: read body: %v", err)
		return
	}
	var entries []rawEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		log.Printf("ingest: bad JSON: %v (body: %.200s)", err, body)
		return
	}

	// The batch's heartbeat entry identifies which gateway sent it.
	gatewayMAC := ""
	for _, e := range entries {
		if e.Gateway != "" {
			gatewayMAC = normalizeMAC(e.Gateway)
			s.tracker.Heartbeat(gatewayMAC)
		}
	}
	if gatewayMAC == "" {
		log.Printf("ingest: batch without gateway heartbeat, dropping %d entries", len(entries))
		return
	}

	sightings := make([]Sighting, 0, len(entries))
	for _, e := range entries {
		if e.MAC == "" || e.Raw == "" {
			continue
		}
		adv, err := base64.StdEncoding.DecodeString(e.Raw)
		if err != nil {
			log.Printf("ingest: bad base64 from %s: %v", e.MAC, err)
			continue
		}
		b, ok := parseIBeacon(adv)
		if !ok {
			continue // Eddystone or other non-iBeacon frame
		}
		if !strings.EqualFold(b.UUID, fleetUUID) {
			continue // ambient iBeacon, not ours
		}
		sightings = append(sightings, s.tracker.Observe(b.Minor, gatewayMAC, e.RSSI))
		if s.verbose {
			log.Printf("sighting: minor=%d (%s) rssi=%d via %s", b.Minor, assetName(b.Minor), e.RSSI, gatewayMAC)
		}
	}

	// Persisted after the whole batch is resolved, in one transaction, and
	// outside the tracker's lock. A failing disk is logged like every other
	// ingest problem: zone resolution has already happened in memory and the
	// API keeps answering, so there is nothing to gain from a 500 the gateway
	// would only retry into the same error.
	if s.store != nil {
		if err := s.store.Record(sightings...); err != nil {
			log.Printf("ingest: persist %d sightings from %s: %v", len(sightings), gatewayMAC, err)
		}
	}
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	views := s.tracker.Assets()
	out := views[:0]
	for _, v := range views {
		if q == "" || strings.Contains(strings.ToLower(v.Name), q) {
			out = append(out, v)
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleGateways(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.tracker.Gateways())
}

// handleHealthz is a liveness check for the platform, deliberately not a
// readiness check on the database. If the volume were to fail, the tracker
// would still be resolving zones from memory and still be worth serving —
// failing the check would restart it in a loop and turn a history outage into
// a total one.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := io.WriteString(w, "ok\n"); err != nil {
		log.Printf("healthz: %v", err)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if err := s.tmpl.ExecuteTemplate(w, "index.html", map[string]any{"Zones": zones}); err != nil {
		log.Printf("render index: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}
