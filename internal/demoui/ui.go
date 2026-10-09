// Package demoui serves a local demo shell; data stays behind API authentication.
package demoui

import (
	"embed"
	"net/http"
)

//go:embed assets/*
var assets embed.FS

func Register(mux *http.ServeMux) {
	for path, file := range map[string]string{"/demo": "index.html", "/demo/": "index.html", "/demo/app.js": "app.js", "/demo/style.css": "style.css"} {
		body, err := assets.ReadFile("assets/" + file)
		if err != nil {
			panic("missing embedded demo asset")
		}
		contentType := "text/html; charset=utf-8"
		if file == "app.js" {
			contentType = "text/javascript; charset=utf-8"
		}
		if file == "style.css" {
			contentType = "text/css; charset=utf-8"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
			_, _ = w.Write(body)
		})
	}
}
