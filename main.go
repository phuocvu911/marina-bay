package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	defaultAddr := os.Getenv("LISTEN_ADDR")
	if defaultAddr == "" {
		defaultAddr = ":8080"
	}
	addr := flag.String("addr", defaultAddr, "listen address (or set LISTEN_ADDR)")
	verbose := flag.Bool("verbose", false, "log every beacon sighting")
	flag.Parse()

	srv := NewServer(NewTracker(), *verbose)

	log.Printf("marina asset tracker listening on %s (POST /ingest, GET /, GET /api/assets)", *addr)
	if err := http.ListenAndServe(*addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
