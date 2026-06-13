package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/scheduler"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/server"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/tablo"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	logger, err := logging.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tabloService := tablo.New(cfg, logger)
	lineupScheduler := scheduler.New(cfg.OutDir, "schedule_lineup.json", "channel lineup update", cfg.LineupInterval, logger, tabloService.MakeLineup)
	guideScheduler := scheduler.New(cfg.OutDir, "schedule_guide.json", "guide data update", cfg.GuideInterval, logger, tabloService.CacheGuideData)

	if err := tabloService.EnsureCredentials(ctx); err != nil {
		logger.Error("credentials setup failed: %v", err)
		os.Exit(1)
	}
	if cfg.ForceCreds {
		if err := lineupScheduler.RunNow(ctx); err != nil {
			logger.Error("lineup update failed: %v", err)
			os.Exit(1)
		}
		logger.Info("Forced credentials creation complete.")
		return
	}
	if cfg.ForceLineup {
		if err := lineupScheduler.RunNow(ctx); err != nil {
			logger.Error("lineup update failed: %v", err)
			os.Exit(1)
		}
		if cfg.CreateXML {
			if err := guideScheduler.RunNow(ctx); err != nil {
				logger.Error("guide update failed: %v", err)
				os.Exit(1)
			}
		}
		logger.Info("Forced lineup update complete.")
		return
	}
	if !tabloService.LineupExists() {
		if err := lineupScheduler.RunNow(ctx); err != nil {
			logger.Error("initial lineup update failed: %v", err)
			os.Exit(1)
		}
	}
	if err := tabloService.LoadLineup(); err != nil {
		logger.Error("could not read lineup file: %v", err)
		os.Exit(1)
	}
	if cfg.CreateXML && !tabloService.GuideExists() {
		if err := guideScheduler.RunNow(ctx); err != nil {
			logger.Error("initial guide update failed: %v", err)
			os.Exit(1)
		}
	}
	lineupScheduler.Start(ctx)
	if cfg.CreateXML {
		guideScheduler.Start(ctx)
	}
	httpServer := server.New(cfg, logger, tabloService)
	if err := httpServer.Run(ctx); err != nil {
		logger.Error("server failed: %v", err)
		os.Exit(1)
	}
}
