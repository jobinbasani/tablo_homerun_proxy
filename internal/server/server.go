package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"sync/atomic"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/tablo"
)

type Server struct {
	cfg       config.Config
	log       *logging.Logger
	tablo     *tablo.Service
	streams   int64
	streamSem chan struct{}
}

func New(cfg config.Config, logger *logging.Logger, tabloService *tablo.Service) *Server {
	tunerCount := tabloService.TunerCount()
	if tunerCount <= 0 {
		tunerCount = 2
	}
	return &Server{
		cfg:       cfg,
		log:       logger,
		tablo:     tabloService,
		streamSem: make(chan struct{}, tunerCount),
	}
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/discover.json", s.handleDiscover)
	mux.HandleFunc("/lineup.json", s.handleLineup)
	mux.HandleFunc("/lineup_status.json", s.handleLineupStatus)
	mux.HandleFunc("/channel/", s.handleChannel)
	if s.cfg.CreateXML {
		mux.HandleFunc("/guide.xml", s.handleGuide)
	}
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
	s.log.Info("Server is running on %s with %d tuners.", s.cfg.ServerURL, s.tablo.TunerCount())
	if s.cfg.CreateXML {
		s.log.Info("Guide data is available at %s/guide.xml.", s.cfg.ServerURL)
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

func (s *Server) handleDiscover(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"FriendlyName":    s.cfg.Name,
		"Manufacturer":    "tablo-homerun-proxy",
		"ModelNumber":     "HDHR3-US",
		"FirmwareName":    "hdhomerun3_atsc",
		"FirmwareVersion": "20240101",
		"DeviceID":        s.cfg.DeviceID,
		"DeviceAuth":      "tabloauth123",
		"BaseURL":         s.cfg.ServerURL,
		"LocalIP":         s.cfg.ServerURL,
		"LineupURL":       s.cfg.ServerURL + "/lineup.json",
		"TunerCount":      s.tablo.TunerCount(),
	})
}

func (s *Server) handleLineup(w http.ResponseWriter, _ *http.Request) {
	lineup := s.tablo.Lineup()
	sort.Slice(lineup, func(i, j int) bool {
		return lineup[i].GuideNumber < lineup[j].GuideNumber
	})
	writeJSON(w, lineup)
}

func (s *Server) handleLineupStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"ScanInProgress": 0,
		"ScanPossible":   1,
		"Source":         "Antenna",
		"SourceList":     []string{"Antenna"},
	})
}

func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, s.tablo.GuidePath())
}

func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
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
	s.log.Info("[%d/%d] client connected to %s.", current, s.tablo.TunerCount(), channelID)
	defer s.log.Info("client disconnected from %s.", channelID)

	cmd := exec.CommandContext(r.Context(), "ffmpeg",
		"-i", playlistURL,
		"-c", "copy",
		"-f", "mpegts",
		"-v", "repeat+level+"+s.cfg.FFmpegLogLevel,
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

const defaultShutdownTimeout = 10_000_000_000
