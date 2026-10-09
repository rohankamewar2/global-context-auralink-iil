// demo-view previews the current embedded UI against the running isolated voice
// rehearsal API. Both listeners are fixed loopback addresses.
package main

import (
	"github.com/rohankamewar2/global-context-auralink-iil/internal/demoui"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func main() {
	target, _ := url.Parse("http://127.0.0.1:8091")
	mux := http.NewServeMux()
	demoui.Register(mux)
	mux.Handle("/v1/", httputil.NewSingleHostReverseProxy(target))
	server := &http.Server{Addr: "127.0.0.1:8092", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
	log.Print("Voice-to-chat demo: http://127.0.0.1:8092/demo")
	log.Fatal(server.ListenAndServe())
}
