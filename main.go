package main

import (
	"flag"
	"log"
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

	srv := NewServer(NewTracker(), *verbose).HTTPServer(*addr)

	log.Printf("asset tracker listening on %s (POST /ingest, GET /, GET /api/assets)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
