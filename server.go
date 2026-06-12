package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"sync/atomic"
)

func (a *App) RunServer(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/discover.json", a.handleDiscover)
	mux.HandleFunc("/lineup.json", a.handleLineup)
	mux.HandleFunc("/lineup_status.json", a.handleLineupStatus)
	mux.HandleFunc("/channel/", a.handleChannel)
	if a.cfg.CreateXML {
		mux.HandleFunc("/guide.xml", a.handleGuide)
	}
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {})

	server := &http.Server{
		Addr:    ":" + a.cfg.Port,
		Handler: withCORS(mux),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	a.log.Info("Server is running on %s with %d tuners.", a.cfg.ServerURL, a.tuners)
	if a.cfg.CreateXML {
		a.log.Info("Guide data is available at %s/guide.xml.", a.cfg.ServerURL)
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

func (a *App) handleDiscover(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"FriendlyName":    a.cfg.Name,
		"Manufacturer":    "tablo-homerun-proxy",
		"ModelNumber":     "HDHR3-US",
		"FirmwareName":    "hdhomerun3_atsc",
		"FirmwareVersion": "20240101",
		"DeviceID":        a.cfg.DeviceID,
		"DeviceAuth":      "tabloauth123",
		"BaseURL":         a.cfg.ServerURL,
		"LocalIP":         a.cfg.ServerURL,
		"LineupURL":       a.cfg.ServerURL + "/lineup.json",
		"TunerCount":      a.tuners,
	})
}

func (a *App) handleLineup(w http.ResponseWriter, _ *http.Request) {
	lineup := make([]LineupEntry, 0, len(a.lineup))
	for _, entry := range a.lineup {
		lineup = append(lineup, entry)
	}
	sort.Slice(lineup, func(i, j int) bool {
		return lineup[i].GuideNumber < lineup[j].GuideNumber
	})
	writeJSON(w, lineup)
}

func (a *App) handleLineupStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"ScanInProgress": 0,
		"ScanPossible":   1,
		"Source":         "Antenna",
		"SourceList":     []string{"Antenna"},
	})
}

func (a *App) handleGuide(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, a.guidePath())
}

func (a *App) handleChannel(w http.ResponseWriter, r *http.Request) {
	channelID := r.URL.Path[len("/channel/"):]
	entry, ok := a.lineup[channelID]
	if !ok {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if entry.Type == "ota" {
		select {
		case a.streamSem <- struct{}{}:
			defer func() { <-a.streamSem }()
		default:
			http.Error(w, "max streams are running", http.StatusServiceUnavailable)
			return
		}
	}
	body := DeviceWatchRequest{
		Bandwidth: nil,
		Extra: map[string]any{
			"limitedAdTracking": 1,
			"deviceOSVersion":   "16.6",
			"lang":              "en_US",
			"height":            1080,
			"deviceId":          "00000000-0000-0000-0000-000000000000",
			"width":             1920,
			"deviceModel":       "iPhone10,1",
			"deviceMake":        "Apple",
			"deviceOS":          "iOS",
		},
		DeviceID: a.creds.UUID,
		Platform: "ios",
	}
	var watch WatchResponse
	err := a.deviceJSON(r.Context(), http.MethodPost, a.creds.Device.URL, "/guide/channels/"+channelID+"/watch", body, &watch)
	if err != nil {
		a.log.Error("watch request failed for %s: %v", channelID, err)
		http.Error(w, "failed to request stream", http.StatusBadGateway)
		return
	}
	if watch.PlaylistURL == "" {
		http.Error(w, "playlist_url missing from Tablo response", http.StatusBadGateway)
		return
	}
	current := atomic.AddInt64(&a.streams, 1)
	defer atomic.AddInt64(&a.streams, -1)
	a.log.Info("[%d/%d] client connected to %s.", current, a.tuners, channelID)
	defer a.log.Info("client disconnected from %s.", channelID)

	cmd := exec.CommandContext(r.Context(), "ffmpeg",
		"-i", watch.PlaylistURL,
		"-c", "copy",
		"-f", "mpegts",
		"-v", "repeat+level+"+a.cfg.FFmpegLogLevel,
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
				a.log.Debug("[ffmpeg] %s", string(data))
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
