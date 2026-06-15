package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/hdhr"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/scheduler"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/server"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/ssdp"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/store"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/tablo"
)

func main() {
	bootCfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	cfgStore, err := store.Open(bootCfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "database error: %v\n", err)
		os.Exit(1)
	}
	defer cfgStore.Close()
	if err := cfgStore.Init(context.Background(), bootCfg); err != nil {
		fmt.Fprintf(os.Stderr, "database initialization error: %v\n", err)
		os.Exit(1)
	}
	cfg, restartPending, err := cfgStore.LoadConfig(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "database config error: %v\n", err)
		os.Exit(1)
	}
	cfg.UserName = bootCfg.UserName
	cfg.UserPass = bootCfg.UserPass
	cfg.ForceCreds = bootCfg.ForceCreds
	cfg.ForceLineup = bootCfg.ForceLineup

	logger, err := logging.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	adminPassword := bootCfg.AdminPassword
	hasAdminPassword, err := cfgStore.HasAdminPassword(ctx)
	if err != nil {
		logger.Error("admin password lookup failed: %v", err)
		os.Exit(1)
	}
	switch {
	case adminPassword != "":
		if err := cfgStore.SetAdminPassword(ctx, adminPassword); err != nil {
			logger.Error("admin password initialization failed: %v", err)
			os.Exit(1)
		}
		logger.Info("Admin password loaded from configuration and saved to the database.")
	case !hasAdminPassword:
		adminPassword, err = randomAdminPassword()
		if err != nil {
			logger.Error("admin password generation failed: %v", err)
			os.Exit(1)
		}
		if err := cfgStore.SetAdminPassword(ctx, adminPassword); err != nil {
			logger.Error("admin password initialization failed: %v", err)
			os.Exit(1)
		}
		logger.Always("Generated admin password: %s", adminPassword)
		logger.Always("Use this password to log in at %s/admin. It has been saved to the database.", cfg.ServerURL)
	default:
		logger.Info("Using admin password from the database.")
	}

	tabloService := tablo.New(cfg, logger, cfgStore)
	lineupScheduler := scheduler.New(cfg.OutDir, "schedule_lineup.json", "channel lineup update", cfg.LineupInterval, logger, tabloService.MakeLineup)
	guideScheduler := scheduler.New(cfg.OutDir, "schedule_guide.json", "guide data update", cfg.GuideInterval, logger, tabloService.CacheGuideData)

	httpServer := server.New(cfg, logger, cfgStore, tabloService, restartPending)
	schedulersStarted := false
	activateProxy := func(ctx context.Context) error {
		if err := tabloService.EnsureCredentialsNonInteractive(ctx); err != nil {
			return err
		}
		activeCfg := tabloService.Config()
		if activeCfg.ForceLineup || !tabloService.LineupExists() {
			if err := lineupScheduler.RunNow(ctx); err != nil {
				return err
			}
		}
		if err := tabloService.LoadLineup(); err != nil {
			return err
		}
		if activeCfg.CreateXML && (activeCfg.ForceLineup || !tabloService.GuideExists()) {
			if err := guideScheduler.RunNow(ctx); err != nil {
				return err
			}
		}
		if !schedulersStarted {
			lineupScheduler.Start(ctx)
			if activeCfg.CreateXML {
				guideScheduler.Start(ctx)
			}
			schedulersStarted = true
		}
		httpServer.SetProxyReady(true)
		logger.Always("Proxy is active.")
		return nil
	}
	httpServer.SetSetupHandler(activateProxy)
	hdhrDiscovery := hdhr.New(httpServer.ConfigSnapshot, httpServer.IsProxyReady, tabloService.TunerCount, logger)
	go hdhrDiscovery.Run(ctx)
	ssdpService := ssdp.New(httpServer.ConfigSnapshot, httpServer.IsProxyReady, logger)
	go ssdpService.Run(ctx)
	if err := activateProxy(ctx); err != nil {
		httpServer.SetProxyReady(false)
		if errors.Is(err, tablo.ErrCredentialsMissing) {
			logger.Always("Admin interface is ready. Configure Tablo credentials to start the proxy.")
		} else {
			logger.Error("proxy activation failed: %v", err)
			logger.Always("Admin interface is ready. Fix setup and retry from /admin.")
		}
	}
	if err := httpServer.Run(ctx); err != nil {
		logger.Error("server failed: %v", err)
		os.Exit(1)
	}
}

func randomAdminPassword() (string, error) {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
