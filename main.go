package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/scheduler"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/server"
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
		logger.Info("Proxy is active.")
		return nil
	}
	httpServer.SetSetupHandler(activateProxy)
	if err := activateProxy(ctx); err != nil {
		httpServer.SetProxyReady(false)
		if errors.Is(err, tablo.ErrCredentialsMissing) {
			logger.Info("Admin interface is ready. Configure Tablo credentials to start the proxy.")
		} else {
			logger.Error("proxy activation failed: %v", err)
			logger.Info("Admin interface is ready. Fix setup and retry from /admin.")
		}
	}
	if err := httpServer.Run(ctx); err != nil {
		logger.Error("server failed: %v", err)
		os.Exit(1)
	}
}
