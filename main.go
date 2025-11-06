package main

import (
	"net/http"
)

func main() {
	// setting up multiplex
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(".")))

	// setting up server
	server := &http.Server{}
	server.Handler = mux
	server.Addr = ":8080"
	server.ListenAndServe()
}
