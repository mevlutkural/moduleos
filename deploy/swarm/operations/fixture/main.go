// Deterministic, test-only HTTP/log fixture. Not a ModuleOS product component.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

var variant = "a"

func main() {
	marker := os.Getenv("OPS_MARKER")
	if marker == "" {
		os.Exit(2)
	}
	if os.Getenv("OPS_CRASH") == "1" {
		log.Print("controlled fixture failure")
		time.Sleep(100 * time.Millisecond)
		os.Exit(23)
	}
	go func() {
		for {
			log.Printf("ops-%s-%s", marker, variant)
			time.Sleep(time.Second)
		}
	}()
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "ops-%s-%s\n", marker, variant) })}
	if server.ListenAndServe() != nil {
		os.Exit(1)
	}
}
