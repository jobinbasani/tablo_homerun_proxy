package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/store"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/tablo"
)

type Server struct {
	cfg        config.Config
	log        *logging.Logger
	tablo      *tablo.Service
	store      *store.Store
	streams    int64
	streamSem  chan struct{}
	restart    bool
	proxyReady bool
	onSetup    func(context.Context) error
	mu         sync.RWMutex
}

func New(cfg config.Config, logger *logging.Logger, cfgStore *store.Store, tabloService *tablo.Service, restartPending bool) *Server {
	tunerCount := tabloService.TunerCount()
	if tunerCount <= 0 {
		tunerCount = 2
	}
	return &Server{
		cfg:       cfg,
		log:       logger,
		tablo:     tabloService,
		store:     cfgStore,
		streamSem: make(chan struct{}, tunerCount),
		restart:   restartPending,
	}
}

func (s *Server) SetSetupHandler(handler func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSetup = handler
}

func (s *Server) SetProxyReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxyReady = ready
}

func (s *Server) ConfigSnapshot() config.Config {
	return s.config()
}

func (s *Server) IsProxyReady() bool {
	return s.isProxyReady()
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin", s.handleAdminIndex)
	mux.HandleFunc("/admin/", s.handleAdminIndex)
	mux.HandleFunc("/admin/assets/", s.handleAdminAsset)
	mux.HandleFunc("/admin/api/login", s.handleAdminLogin)
	mux.HandleFunc("/admin/api/logout", s.requireAdmin(s.handleAdminLogout))
	mux.HandleFunc("/admin/api/session", s.handleAdminSession)
	mux.HandleFunc("/admin/api/password", s.requireAdmin(s.handleAdminPassword))
	mux.HandleFunc("/admin/api/config", s.requireAdmin(s.handleAdminConfig))
	mux.HandleFunc("/admin/api/status", s.requireAdmin(s.handleAdminStatus))
	mux.HandleFunc("/admin/api/hdhomerun/endpoints", s.requireAdmin(s.handleHDHomeRunEndpoints))
	mux.HandleFunc("/admin/api/tablo/login", s.requireAdmin(s.handleTabloLogin))
	mux.HandleFunc("/admin/api/tablo/select-device", s.requireAdmin(s.handleTabloSelectDevice))
	mux.HandleFunc("/admin/api/actions/refresh-lineup", s.requireAdmin(s.handleRefreshLineup))
	mux.HandleFunc("/admin/api/actions/refresh-guide", s.requireAdmin(s.handleRefreshGuide))
	mux.HandleFunc("/discover.json", s.handleDiscover)
	mux.HandleFunc("/lineup.json", s.handleLineup)
	mux.HandleFunc("/lineup_status.json", s.handleLineupStatus)
	mux.HandleFunc("/channel/", s.handleChannel)
	mux.HandleFunc("/guide.xml", s.handleGuide)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {})

	server := &http.Server{
		Addr:    ":" + s.cfg.Port,
		Handler: withCORS(mux),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	cfg := s.config()
	s.log.Always("Server is running on %s with %d tuners.", cfg.ServerURL, s.tablo.TunerCount())
	if cfg.CreateXML {
		s.log.Always("Guide data is available at %s/guide.xml.", cfg.ServerURL)
	}
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.isProxyReady() {
		http.Error(w, "proxy setup required", http.StatusServiceUnavailable)
		return
	}
	s.logClientRequest("discover", r)
	cfg := s.config()
	writeJSON(w, discoveryPayload(cfg, s.tablo.TunerCount()))
}

func (s *Server) handleLineup(w http.ResponseWriter, r *http.Request) {
	if !s.isProxyReady() {
		http.Error(w, "proxy setup required", http.StatusServiceUnavailable)
		return
	}
	s.logClientRequest("lineup", r)
	lineup := s.tablo.Lineup()
	sort.Slice(lineup, func(i, j int) bool {
		return lineup[i].GuideNumber < lineup[j].GuideNumber
	})
	writeJSON(w, lineup)
}

func (s *Server) handleLineupStatus(w http.ResponseWriter, r *http.Request) {
	if !s.isProxyReady() {
		http.Error(w, "proxy setup required", http.StatusServiceUnavailable)
		return
	}
	s.logClientRequest("lineup_status", r)
	writeJSON(w, lineupStatusPayload())
}

func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	if !s.isProxyReady() {
		http.Error(w, "proxy setup required", http.StatusServiceUnavailable)
		return
	}
	s.logClientRequest("guide", r)
	http.ServeFile(w, r, s.tablo.GuidePath())
}

func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
	if !s.isProxyReady() {
		http.Error(w, "proxy setup required", http.StatusServiceUnavailable)
		return
	}
	s.logClientRequest("stream", r)
	channelID := r.URL.Path[len("/channel/"):]
	entry, ok := s.tablo.Channel(channelID)
	if !ok {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if entry.Type == "ota" {
		select {
		case s.streamSem <- struct{}{}:
			defer func() { <-s.streamSem }()
		default:
			http.Error(w, "max streams are running", http.StatusServiceUnavailable)
			return
		}
	}
	_, playlistURL, err := s.tablo.Watch(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, tablo.ErrChannelNotFound) {
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		s.log.Error("watch request failed for %s: %v", channelID, err)
		http.Error(w, "failed to request stream", http.StatusBadGateway)
		return
	}
	current := atomic.AddInt64(&s.streams, 1)
	defer atomic.AddInt64(&s.streams, -1)
	client := clientDescription(r)
	s.log.Info("[%d/%d] client connected to %s from %s.", current, s.tablo.TunerCount(), channelID, client)
	defer s.log.Info("client disconnected from %s from %s.", channelID, client)

	cmd := exec.CommandContext(r.Context(), "ffmpeg",
		"-i", playlistURL,
		"-c", "copy",
		"-f", "mpegts",
		"-v", "repeat+level+"+s.config().FFmpegLogLevel,
		"pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to start stream", http.StatusInternalServerError)
		return
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start ffmpeg", http.StatusInternalServerError)
		return
	}
	go func() {
		if stderr != nil {
			data, _ := io.ReadAll(stderr)
			if len(data) > 0 {
				s.log.Debug("[ffmpeg] %s", string(data))
			}
		}
	}()
	w.Header().Set("Content-Type", "video/mp2t")
	_, _ = io.Copy(w, stdout)
	_ = cmd.Wait()
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func discoveryPayload(cfg config.Config, tunerCount int) map[string]any {
	return map[string]any{
		"FriendlyName":    cfg.Name,
		"Manufacturer":    "tablo-homerun-proxy",
		"ModelNumber":     "HDHR3-US",
		"FirmwareName":    "hdhomerun3_atsc",
		"FirmwareVersion": "20240101",
		"DeviceID":        cfg.DeviceID,
		"DeviceAuth":      "tabloauth123",
		"BaseURL":         cfg.ServerURL,
		"LocalIP":         cfg.IPAddress,
		"LineupURL":       cfg.ServerURL + "/lineup.json",
		"TunerCount":      tunerCount,
	}
}

func lineupStatusPayload() map[string]any {
	return map[string]any{
		"ScanInProgress": 0,
		"ScanPossible":   1,
		"Source":         "Antenna",
		"SourceList":     []string{"Antenna"},
	}
}

func (s *Server) logClientRequest(kind string, r *http.Request) {
	client := clientDescription(r)
	if looksLikePlex(r) {
		s.log.Always("Plex client requested %s from %s.", kind, client)
		return
	}
	s.log.Info("HDHomeRun client requested %s from %s.", kind, client)
}

func clientDescription(r *http.Request) string {
	host := r.RemoteAddr
	if parsedHost, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = parsedHost
	}
	userAgent := strings.TrimSpace(r.UserAgent())
	if userAgent == "" {
		userAgent = "unknown user-agent"
	}
	return host + " (" + userAgent + ")"
}

func looksLikePlex(r *http.Request) bool {
	if strings.Contains(strings.ToLower(r.UserAgent()), "plex") {
		return true
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-plex-") {
			return true
		}
	}
	return false
}

func (s *Server) config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Server) setConfig(cfg config.Config, restartPending bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.restart = restartPending
}

func (s *Server) setupHandler() func(context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onSetup
}

func (s *Server) isProxyReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyReady
}

const defaultShutdownTimeout = 10_000_000_000
