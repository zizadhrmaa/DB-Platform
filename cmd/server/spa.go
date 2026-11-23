package main

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/web"
)

func spaHandler() http.Handler {
	dist, _ := fs.Sub(web.DistFS, "dist")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/app")), "/")
		if name != "" && name != "index.html" && !strings.HasPrefix(path.Base(name), ".") {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeFileFS(w, r, dist, name)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "Frontend belum dibangun. Jalankan make fe-build.", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(index)
	})
}
