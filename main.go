// Command task051-base62 runs the Base62 codec and short-code registry service.
//
// Use --smoke-test to run the built-in self-check, which exits the process on
// completion. Without flags it starts an HTTP server on the address given by
// the --addr flag (default ":8080").
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"task051-base62/internal/httpapi"
	"task051-base62/internal/selfcheck"
)

func main() {
	smoke := flag.Bool("smoke-test", false, "run the built-in self-check and exit")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	if *smoke {
		if err := selfcheck.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "smoke-test FAILED: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("smoke-test PASSED")
		return
	}

	srv := httpapi.NewServer()
	log.Printf("base62 service listening on %s", *addr)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
