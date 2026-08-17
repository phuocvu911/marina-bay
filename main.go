package main

import (
	"flag"
	"log"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address (or set PORT / LISTEN_ADDR)")
	dbPath := flag.String("db", defaultDBPath(), `SQLite database path (or set DB_PATH); "" runs without persistence`)
	retention := flag.Duration("retention", time.Hour, "how long raw sightings are kept before being swept")
	verbose := flag.Bool("verbose", false, "log every beacon sighting")
	flag.Parse()

	tracker := NewTracker()

	var store *Store
	if *dbPath == "" {
		log.Print(`persistence disabled (-db ""): state is in memory only and is lost on restart`)
	} else {
		s, err := OpenStore(*dbPath)
		if err != nil {
			// Fatal on purpose. Starting anyway would look healthy while
			// quietly dropping every asset's last-known position at the next
			// restart; run with -db "" if that is genuinely what you want.
			log.Fatalf("open database: %v", err)
		}
		// Deliberately never closed: ListenAndServe blocks until log.Fatal
		// exits the process, so a deferred Close here would only look like
		// cleanup. Nothing is lost by it — every batch is committed as it
		// arrives, and SQLite recovers its write-ahead log on the next open.
		store = s

		// Seed the resolver before the first gateway reports, so the UI shows
		// last-known positions straight after a redeploy rather than a fleet
		// of never-seen assets.
		states, err := store.LoadAssetState()
		if err != nil {
			log.Printf("restore asset state: %v (starting with an empty tracker)", err)
		} else {
			tracker.Restore(states)
			log.Printf("restored %d assets from %s", len(states), *dbPath)
		}

		startRetentionSweeper(store.DB(), *retention)
	}

	srv := NewServer(tracker, store, *verbose).HTTPServer(*addr)

	log.Printf("asset tracker listening on %s (POST /ingest, GET /, GET /api/assets, GET /healthz)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// defaultAddr resolves the listen address. Fly.io and most other hosts inject
// PORT; LISTEN_ADDR stays supported and wins, so existing local setups and the
// -addr flag keep behaving exactly as they did.
func defaultAddr() string {
	if a := os.Getenv("LISTEN_ADDR"); a != "" {
		return a
	}
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

// defaultDBPath points at the mounted volume, which is where the database has
// to live for it to outlast a machine. Override with -db ./marina.db locally.
func defaultDBPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "/data/marina.db"
}
