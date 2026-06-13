package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var adminFiles embed.FS

func (s *Server) handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	ok, _ := s.authenticated(r)
	if !ok && r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	data, err := adminFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "admin UI unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleAdminAsset(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(adminFiles, "web")
	if err != nil {
		http.Error(w, "admin assets unavailable", http.StatusInternalServerError)
		return
	}
	http.StripPrefix("/admin/assets/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}
