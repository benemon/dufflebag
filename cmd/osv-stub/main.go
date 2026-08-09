package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/benemon/dufflebag/internal/scan"
)

func main() {
	address := flag.String("listen", ":8080", "HTTP listen address")
	flag.Parse()

	server := &http.Server{Addr: *address, Handler: scan.NewOSVStub(nil)}
	log.Printf("recorded OSV stub listening on %s", *address)
	log.Fatal(server.ListenAndServe())
}
