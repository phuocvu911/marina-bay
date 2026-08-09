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
)

//go:embed templates static
var uiFS embed.FS

// Server carries handler dependencies; no globals beyond the registry maps.
type Server struct {
	tracker *Tracker
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

func NewServer(tracker *Tracker, verbose bool) *Server {
	return &Server{
		tracker: tracker,
		tmpl:    template.Must(template.New("ui").Funcs(tmplFuncs).ParseFS(uiFS, "templates/*.html")),
		verbose: verbose,
	}
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest", s.handleIngest)
	mux.HandleFunc("GET /api/assets", s.handleAssets)
	mux.HandleFunc("GET /api/gateways", s.handleGateways)
	mux.Handle("GET /static/", http.FileServer(http.FS(uiFS)))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	return mux
}

// handleIngest accepts a G1-E payload. It always answers 200 — the gateway
// must keep sending even if we couldn't make sense of one batch — so parse
// errors are logged, never returned.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)

	body, err := io.ReadAll(r.Body)
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
		s.tracker.Observe(b.Minor, gatewayMAC, e.RSSI)
		if s.verbose {
			log.Printf("sighting: minor=%d (%s) rssi=%d via %s", b.Minor, assetName(b.Minor), e.RSSI, gatewayMAC)
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
