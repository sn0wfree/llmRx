package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an HTTP handler that serves the embedded SPA.
// Unknown paths fall back to index.html so client-side hash routes
// (e.g. /admin/channels) work on refresh.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	indexBytes, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("webui: missing dist/index.html — did you run `npm run build`?")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/admin")
		if rel == "" {
			rel = "/"
		}
		if rel == "/" {
			writeIndex(w, indexBytes)
			return
		}
		clean := strings.TrimPrefix(rel, "/")
		f, err := sub.Open(clean)
		if err != nil {
			writeIndex(w, indexBytes)
			return
		}
		defer f.Close()
		switch path.Ext(clean) {
		case ".js":
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case ".svg":
			w.Header().Set("Content-Type", "image/svg+xml")
		case ".json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		case ".ico":
			w.Header().Set("Content-Type", "image/x-icon")
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.Copy(w, f)
	})
}

func writeIndex(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}