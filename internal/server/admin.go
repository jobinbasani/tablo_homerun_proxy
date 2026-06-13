package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/store"
)

const adminCookieName = "tablo_admin_session"

type configResponse struct {
	Config         adminConfig     `json:"config"`
	RestartPending bool            `json:"restartPending"`
	RestartFields  map[string]bool `json:"restartFields"`
}

type adminConfig struct {
	Name                 string `json:"Name"`
	DeviceID             string `json:"DeviceID"`
	Port                 string `json:"Port"`
	LineupIntervalDays   int    `json:"LineupIntervalDays"`
	CreateXML            bool   `json:"CreateXML"`
	GuideDays            int    `json:"GuideDays"`
	IncludePseudoTVGuide bool   `json:"IncludePseudoTVGuide"`
	LogLevel             string `json:"LogLevel"`
	SaveLog              bool   `json:"SaveLog"`
	OutDir               string `json:"OutDir"`
	TabloDevice          string `json:"TabloDevice"`
	IPAddress            string `json:"IPAddress"`
	GuideIntervalHours   int    `json:"GuideIntervalHours"`
	IncludeOTT           bool   `json:"IncludeOTT"`
	DBPath               string `json:"DBPath"`
	ServerURL            string `json:"ServerURL"`
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	ok, err := s.store.CheckAdminPassword(r.Context(), req.Password)
	if errors.Is(err, store.ErrAdminPasswordNotSet) {
		if req.Password == "" {
			http.Error(w, "admin password is not configured", http.StatusUnauthorized)
			return
		}
		if err := s.store.SetAdminPassword(r.Context(), req.Password); err != nil {
			http.Error(w, "could not initialize admin password", http.StatusInternalServerError)
			return
		}
		ok = true
	} else if err != nil {
		http.Error(w, "auth failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	token, expiresAt, err := s.store.CreateSession(r.Context())
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/admin",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = s.store.ClearSession(r.Context())
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	authenticated, _ := s.authenticated(r)
	hasPassword, _ := s.store.HasAdminPassword(r.Context())
	writeJSON(w, map[string]any{"authenticated": authenticated, "passwordConfigured": hasPassword})
}

func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := publicConfig(s.config())
		writeJSON(w, configResponse{Config: cfg, RestartPending: s.restartPending(), RestartFields: restartFieldMap()})
	case http.MethodPut:
		var req adminConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		current := s.config()
		next := mergeManagedConfig(current, configFromAdmin(req, current))
		restartPending := s.restartPending() || requiresRestart(current, next)
		next.AdminPassword = ""
		next.UserPass = ""
		if err := s.store.SaveConfig(r.Context(), next, restartPending); err != nil {
			http.Error(w, "could not save config", http.StatusInternalServerError)
			return
		}
		s.setConfig(next, restartPending)
		s.tablo.SetConfig(next)
		writeJSON(w, configResponse{Config: publicConfig(next), RestartPending: restartPending, RestartFields: restartFieldMap()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	writeJSON(w, map[string]any{
		"serverURL":      cfg.ServerURL,
		"proxyReady":     s.isProxyReady(),
		"tunerCount":     s.tablo.TunerCount(),
		"activeStreams":  s.activeStreams(),
		"lineupLoaded":   len(s.tablo.Lineup()) > 0,
		"lineupExists":   s.tablo.LineupExists(),
		"guideExists":    s.tablo.GuideExists(),
		"createXML":      cfg.CreateXML,
		"includeOTT":     cfg.IncludeOTT,
		"restartPending": s.restartPending(),
	})
}

func (s *Server) handleAdminLogs(w http.ResponseWriter, _ *http.Request) {
	lines, err := s.log.RecentLines(200)
	if err != nil {
		http.Error(w, "could not read logs", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"lines": lines})
}

func (s *Server) handleTabloLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	devices, err := s.tablo.LoginForDevices(r.Context(), req.Username, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"devices": devices})
}

func (s *Server) handleTabloSelectDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ServerID string `json:"serverId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.tablo.SelectDevice(r.Context(), req.ServerID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := s.config()
	cfg.TabloDevice = req.ServerID
	cfg = config.ApplyDerived(cfg)
	if err := s.store.SaveConfig(r.Context(), cfg, s.restartPending()); err != nil {
		http.Error(w, "could not save selected device", http.StatusInternalServerError)
		return
	}
	s.setConfig(cfg, s.restartPending())
	s.tablo.SetConfig(cfg)
	if handler := s.setupHandler(); handler != nil {
		if err := handler(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRefreshLineup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.tablo.MakeLineup(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRefreshGuide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.tablo.CacheGuideData(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.authenticated(r)
		if err != nil || !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) authenticated(r *http.Request) (bool, error) {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		return false, nil
	}
	return s.store.CheckSession(r.Context(), cookie.Value)
}

func (s *Server) activeStreams() int64 {
	return s.streams
}

func (s *Server) restartPending() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.restart
}

func publicConfig(cfg config.Config) adminConfig {
	return adminConfig{
		Name:                 cfg.Name,
		DeviceID:             cfg.DeviceID,
		Port:                 cfg.Port,
		LineupIntervalDays:   int(cfg.LineupInterval / (24 * time.Hour)),
		CreateXML:            cfg.CreateXML,
		GuideDays:            cfg.GuideDays,
		IncludePseudoTVGuide: cfg.IncludePseudoTVGuide,
		LogLevel:             cfg.LogLevel,
		SaveLog:              cfg.SaveLog,
		OutDir:               cfg.OutDir,
		TabloDevice:          cfg.TabloDevice,
		IPAddress:            cfg.IPAddress,
		GuideIntervalHours:   int(cfg.GuideInterval / time.Hour),
		IncludeOTT:           cfg.IncludeOTT,
		DBPath:               cfg.DBPath,
		ServerURL:            cfg.ServerURL,
	}
}

func configFromAdmin(req adminConfig, current config.Config) config.Config {
	next := current
	next.Name = req.Name
	next.DeviceID = req.DeviceID
	next.Port = req.Port
	next.LineupInterval = time.Duration(req.LineupIntervalDays) * 24 * time.Hour
	next.CreateXML = req.CreateXML
	next.GuideDays = req.GuideDays
	next.IncludePseudoTVGuide = req.IncludePseudoTVGuide
	next.LogLevel = req.LogLevel
	next.SaveLog = req.SaveLog
	next.OutDir = req.OutDir
	next.TabloDevice = req.TabloDevice
	next.IPAddress = req.IPAddress
	next.GuideInterval = time.Duration(req.GuideIntervalHours) * time.Hour
	next.IncludeOTT = req.IncludeOTT
	next.DBPath = req.DBPath
	return next
}

func mergeManagedConfig(current, next config.Config) config.Config {
	next.ForceCreds = current.ForceCreds
	next.ForceLineup = current.ForceLineup
	next.EnvPath = current.EnvPath
	next.DBPath = current.DBPath
	if next.OutDir == "" {
		next.OutDir = current.OutDir
	}
	if next.Port == "" {
		next.Port = current.Port
	}
	if next.IPAddress == "" {
		next.IPAddress = current.IPAddress
	}
	return config.ApplyDerived(next)
}

func requiresRestart(current, next config.Config) bool {
	return current.Port != next.Port ||
		current.OutDir != next.OutDir ||
		current.DBPath != next.DBPath
}

func restartFieldMap() map[string]bool {
	return map[string]bool{
		"Port":   true,
		"OutDir": true,
		"DBPath": true,
	}
}
